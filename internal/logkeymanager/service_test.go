package logkeymanager

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	key       LogKey
	keys      []LogKey
	keyHash   string
	accountID uuid.UUID
	id        uuid.UUID
	ctx       context.Context
	calls     int
	err       error
}

func (s *storeStub) CreateLogKey(ctx context.Context, key LogKey, keyHash string) (LogKey, error) {
	s.calls++
	s.ctx, s.keyHash, s.key = ctx, keyHash, key
	s.key.ID = uuid.New()
	return s.key, s.err
}

func (s *storeStub) GetLogKeysByAccountID(ctx context.Context, accountID uuid.UUID) ([]LogKey, error) {
	s.calls++
	s.ctx, s.accountID = ctx, accountID
	return s.keys, s.err
}

func (s *storeStub) GetLogKeyByID(ctx context.Context, id uuid.UUID) (LogKey, error) {
	s.calls++
	s.ctx, s.id = ctx, id
	return s.key, s.err
}

func (s *storeStub) SoftDeleteLogKey(ctx context.Context, id uuid.UUID) error {
	s.calls++
	s.ctx, s.id = ctx, id
	return s.err
}

func TestCreateLogKey(t *testing.T) {
	accountID := uuid.New()
	seen := make(map[string]bool)
	for _, scopes := range [][]string{nil, {"write"}, {"read"}, {"read", "write"}} {
		store := &storeStub{}
		service := NewService(store)
		before := time.Now().UTC().Truncate(time.Second)
		key, err := service.CreateLogKey(t.Context(), accountID, scopes)
		if err != nil {
			t.Fatal(err)
		}
		wantScopes := scopes
		if wantScopes == nil {
			wantScopes = []string{"write"}
		}
		if key.ID == uuid.Nil || key.AccountID != accountID.String() ||
			!slices.Equal(key.Scopes, wantScopes) || key.SoftDeleted || key.DeactivatedAt != nil {
			t.Fatalf("unexpected created key: %+v", key.LogKey)
		}
		if key.CreatedAt.Before(before) || key.CreatedAt.After(time.Now()) ||
			!key.ExpiresAt.Equal(key.CreatedAt.Add(30*24*time.Hour)) ||
			key.RateLimitWindow != 60 || key.RateLimitMax != 100 {
			t.Fatalf("unexpected key defaults: %+v", key.LogKey)
		}
		if !strings.HasPrefix(key.APIKey, "log_") {
			t.Fatal("key prefix is missing")
		}
		randomBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key.APIKey, "log_"))
		if err != nil || len(randomBytes) != 32 || seen[key.APIKey] {
			t.Fatal("key does not contain a fresh 32-byte random value")
		}
		seen[key.APIKey] = true
		digest := sha256.Sum256([]byte(key.APIKey))
		if store.keyHash != hex.EncodeToString(digest[:]) || store.keyHash == key.APIKey ||
			store.ctx != t.Context() {
			t.Fatal("store did not receive only the hash and caller context")
		}
	}
}

func TestCreateLogKeyRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name      string
		accountID uuid.UUID
		scopes    []string
		wantErr   error
	}{
		{name: "nil account", wantErr: ErrInvalidAccountID},
		{name: "empty scopes", accountID: uuid.New(), scopes: []string{}, wantErr: ErrInvalidScopes},
		{name: "unknown scope", accountID: uuid.New(), scopes: []string{"admin"}, wantErr: ErrInvalidScopes},
		{name: "duplicate scopes", accountID: uuid.New(), scopes: []string{"write", "write"}, wantErr: ErrInvalidScopes},
		{name: "too many scopes", accountID: uuid.New(), scopes: []string{"read", "write", "read"}, wantErr: ErrInvalidScopes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &storeStub{}
			_, err := NewService(store).CreateLogKey(t.Context(), tt.accountID, tt.scopes)
			if !errors.Is(err, tt.wantErr) || store.calls != 0 {
				t.Fatalf("error = %v, store calls = %d", err, store.calls)
			}
		})
	}
}

func TestLogKeyOperations(t *testing.T) {
	accountID, id := uuid.New(), uuid.New()
	store := &storeStub{key: LogKey{ID: id}}
	service := NewService(store)
	keys, err := service.GetLogKeysByAccountID(t.Context(), accountID)
	if err != nil || keys == nil || len(keys) != 0 || store.accountID != accountID {
		t.Fatalf("account keys = %v, error = %v", keys, err)
	}
	key, err := service.GetLogKeyByID(t.Context(), id)
	if err != nil || key.ID != id || store.id != id {
		t.Fatalf("key = %+v, error = %v", key, err)
	}
	if err := service.DeleteLogKey(t.Context(), id); err != nil || store.id != id {
		t.Fatalf("delete error = %v", err)
	}
	if store.ctx != t.Context() {
		t.Fatal("operation lost the caller context")
	}
}

func TestLogKeyOperationsRejectNilIDs(t *testing.T) {
	store := &storeStub{}
	service := NewService(store)
	if _, err := service.GetLogKeysByAccountID(t.Context(), uuid.Nil); !errors.Is(err, ErrInvalidAccountID) {
		t.Fatal(err)
	}
	if _, err := service.GetLogKeyByID(t.Context(), uuid.Nil); !errors.Is(err, ErrInvalidKeyID) {
		t.Fatal(err)
	}
	if err := service.DeleteLogKey(t.Context(), uuid.Nil); !errors.Is(err, ErrInvalidKeyID) {
		t.Fatal(err)
	}
	if store.calls != 0 {
		t.Fatal("invalid IDs reached the store")
	}
}

func TestLogKeyOperationsPreserveStoreErrors(t *testing.T) {
	failure := errors.New("store unavailable")
	store := &storeStub{err: failure}
	service := NewService(store)
	key, err := service.CreateLogKey(t.Context(), uuid.New(), nil)
	if !errors.Is(err, failure) || key.APIKey != "" {
		t.Fatalf("create error = %v, returned key = %q", err, key.APIKey)
	}
	if _, err := service.GetLogKeysByAccountID(t.Context(), uuid.New()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := service.GetLogKeyByID(t.Context(), uuid.New()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := service.DeleteLogKey(t.Context(), uuid.New()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
