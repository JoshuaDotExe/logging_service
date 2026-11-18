package authoriser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

type readerStub struct {
	credentials Credentials
	err         error
	called      bool
	hash        string
	ctx         context.Context
}

func (s *readerStub) GetAPIKeyCredentials(ctx context.Context, hash string) (Credentials, error) {
	s.called, s.hash, s.ctx = true, hash, ctx
	return s.credentials, s.err
}

func TestVerifyAPIKeyReturnsKeyAndStoredScopes(t *testing.T) {
	rawKey := "log_test-key"
	for _, scopes := range [][]string{{"read", "write"}, {}} {
		reader := &readerStub{credentials: Credentials{AccountID: "account", Scopes: scopes}}
		service := NewService(reader)
		ctx := t.Context()
		got, err := service.VerifyAPIKey(ctx, rawKey)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(rawKey))
		if reader.hash != hex.EncodeToString(digest[:]) || reader.ctx != ctx {
			t.Fatal("reader did not receive the key hash and caller context")
		}
		want := APIKey{Key: rawKey, AccountID: "account", Scopes: scopes}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("key = %+v, want %+v", got, want)
		}
	}
}

func TestVerifyAPIKeyErrors(t *testing.T) {
	databaseError := errors.New("database unavailable")
	for _, tt := range []struct {
		name       string
		key        string
		readerErr  error
		wantErr    error
		wantCalled bool
	}{
		{name: "empty", wantErr: ErrInvalidAPIKey},
		{name: "unknown or inactive", key: "log_missing", readerErr: ErrInvalidAPIKey, wantErr: ErrInvalidAPIKey, wantCalled: true},
		{name: "database error", key: "log_key", readerErr: databaseError, wantErr: databaseError, wantCalled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &readerStub{err: tt.readerErr}
			got, err := NewService(reader).VerifyAPIKey(t.Context(), tt.key)
			if !errors.Is(err, tt.wantErr) || reader.called != tt.wantCalled {
				t.Fatalf("error = %v, reader called = %v; want %v, %v", err, reader.called, tt.wantErr, tt.wantCalled)
			}
			if got.Key != "" || got.AccountID != "" || got.Scopes != nil {
				t.Fatalf("error returned credentials: %+v", got)
			}
		})
	}
}
