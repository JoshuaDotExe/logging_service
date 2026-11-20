package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"logging-service/internal/logkeymanager"
	"logging-service/internal/tokenhandler"

	"github.com/google/uuid"
)

const testManagementKey = "test-management-key"
const testAccountID = "00000000-0000-4000-8000-000000000001"

type authStub struct{ token string }

func (a authStub) Verify(raw string) (uuid.UUID, error) {
	if a.token == "" || raw != a.token {
		return uuid.Nil, tokenhandler.ErrInvalidToken
	}
	return uuid.MustParse(testAccountID), nil
}

func (a authStub) GetActiveAccount(_ context.Context, id uuid.UUID) (tokenhandler.Account, error) {
	return tokenhandler.Account{ID: id, Role: "member"}, nil
}

type storeStub struct {
	key       logkeymanager.LogKey
	keys      []logkeymanager.LogKey
	accountID uuid.UUID
	id        uuid.UUID
	calls     int
	err       error
	ctx       context.Context
}

func (s *storeStub) CreateLogKey(ctx context.Context, key logkeymanager.LogKey, _ string) (logkeymanager.LogKey, error) {
	s.calls++
	key.ID = uuid.New()
	s.key, s.ctx = key, ctx
	return key, s.err
}

func (s *storeStub) GetLogKeysByAccountID(_ context.Context, accountID uuid.UUID) ([]logkeymanager.LogKey, error) {
	s.calls++
	s.accountID = accountID
	return s.keys, s.err
}

func (s *storeStub) GetLogKeyByID(_ context.Context, id uuid.UUID) (logkeymanager.LogKey, error) {
	s.calls++
	s.id = id
	return s.key, s.err
}

func (s *storeStub) SoftDeleteLogKey(_ context.Context, id uuid.UUID) error {
	s.calls++
	s.id = id
	return s.err
}

func testRouter(store *storeStub, token string) http.Handler {
	auth := authStub{token: token}
	return NewRouter(logkeymanager.NewService(store), slog.New(slog.NewTextHandler(io.Discard, nil)), auth, auth)
}

func serveRequest(router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestCreateLogKeyEndpoint(t *testing.T) {
	accountID := testAccountID
	for _, tt := range []struct {
		name       string
		body       string
		wantStatus int
		scopes     []string
	}{
		{name: "default scope", body: fmt.Sprintf(`{"account_id":%q}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"write"}},
		{name: "read scope", body: fmt.Sprintf(`{"account_id":%q,"scopes":["read"]}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"read"}},
		{name: "write scope", body: fmt.Sprintf(`{"account_id":%q,"scopes":["write"]}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"write"}},
		{name: "delete scope", body: fmt.Sprintf(`{"account_id":%q,"scopes":["delete"]}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"delete"}},
		{name: "both scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":["write","read"]}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"write", "read"}},
		{name: "all scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":["write","read","delete"]}`, accountID), wantStatus: http.StatusCreated, scopes: []string{"write", "read", "delete"}},
		{name: "account derived from token", body: `{}`, wantStatus: http.StatusCreated, scopes: []string{"write"}},
		{name: "invalid account", body: `{"account_id":"invalid"}`, wantStatus: http.StatusBadRequest},
		{name: "nil account", body: fmt.Sprintf(`{"account_id":%q}`, uuid.Nil.String()), wantStatus: http.StatusBadRequest},
		{name: "empty scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":[]}`, accountID), wantStatus: http.StatusBadRequest},
		{name: "null scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":null}`, accountID), wantStatus: http.StatusBadRequest},
		{name: "non-array scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":"write"}`, accountID), wantStatus: http.StatusBadRequest},
		{name: "unknown scope", body: fmt.Sprintf(`{"account_id":%q,"scopes":["admin"]}`, accountID), wantStatus: http.StatusBadRequest},
		{name: "duplicate scopes", body: fmt.Sprintf(`{"account_id":%q,"scopes":["write","write"]}`, accountID), wantStatus: http.StatusBadRequest},
		{name: "malformed request", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "oversized request", body: `{"account_id":"` + strings.Repeat("a", 4096) + `"}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &storeStub{}
			response := serveRequest(testRouter(store, testManagementKey), http.MethodPost, "/logkey", tt.body, testManagementKey)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("key response is cacheable")
			}
			if response.Code == http.StatusCreated {
				var key logkeymanager.CreatedLogKey
				if err := json.Unmarshal(response.Body.Bytes(), &key); err != nil {
					t.Fatal(err)
				}
				if key.APIKey == "" || key.ID == uuid.Nil || key.AccountID != accountID || !slices.Equal(key.Scopes, tt.scopes) {
					t.Fatalf("invalid key response: %+v", key.LogKey)
				}
				if _, ok := store.ctx.Deadline(); !ok {
					t.Fatal("request did not propagate a deadline")
				}
			} else if store.calls != 0 {
				t.Fatal("invalid input reached the store")
			}
		})
	}
}

func TestManagementAuthorization(t *testing.T) {
	id, accountID := uuid.NewString(), uuid.NewString()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/logkey"},
		{http.MethodGet, "/logkey/" + id},
		{http.MethodGet, "/logkey/account/" + accountID},
		{http.MethodDelete, "/logkey/" + id},
	} {
		for _, token := range []string{"", "wrong-key"} {
			store := &storeStub{}
			response := serveRequest(testRouter(store, testManagementKey), route.method, route.path, `{}`, token)
			if response.Code != http.StatusUnauthorized || store.calls != 0 {
				t.Fatalf("%s %s status = %d, store calls = %d", route.method, route.path, response.Code, store.calls)
			}
		}
	}
	store := &storeStub{}
	router := testRouter(store, "")
	response := serveRequest(router, http.MethodPost, "/logkey", `{}`, "")
	if response.Code != http.StatusUnauthorized || store.calls != 0 {
		t.Fatal("unconfigured management authorization did not fail closed")
	}
	response = serveRequest(router, http.MethodGet, "/health", "", "")
	if response.Code != http.StatusOK || store.calls != 0 {
		t.Fatal("health endpoint required management authorization")
	}
}

func TestReadAndDeleteLogKeyEndpoints(t *testing.T) {
	id, accountID := uuid.New(), uuid.MustParse(testAccountID)
	store := &storeStub{
		key: logkeymanager.LogKey{ID: id, AccountID: accountID.String(), Scopes: []string{"write"}, SoftDeleted: true},
	}
	store.keys = []logkeymanager.LogKey{store.key}
	router := testRouter(store, testManagementKey)
	for _, path := range []string{"/logkey/" + id.String(), "/logkey/account/" + accountID.String()} {
		response := serveRequest(router, http.MethodGet, path, "", testManagementKey)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"api_key"`) ||
			strings.Contains(response.Body.String(), `"key_hash"`) {
			t.Fatalf("unsafe metadata response: %d %s", response.Code, response.Body.String())
		}
	}
	if store.id != id || store.accountID != accountID {
		t.Fatal("metadata routes did not pass the requested IDs")
	}
	response := serveRequest(router, http.MethodDelete, "/logkey/"+id.String(), "", testManagementKey)
	if response.Code != http.StatusNoContent || store.id != id {
		t.Fatalf("delete status = %d, id = %s", response.Code, store.id)
	}
	store.keys = nil
	response = serveRequest(router, http.MethodGet, "/logkey/account/"+accountID.String(), "", testManagementKey)
	if response.Code != http.StatusOK || response.Body.String() != `{"keys":[]}` {
		t.Fatalf("empty keys response = %s", response.Body.String())
	}
}

func TestLogKeyEndpointErrors(t *testing.T) {
	id := uuid.NewString()
	for _, tt := range []struct {
		name       string
		method     string
		path       string
		body       string
		storeErr   error
		wantStatus int
	}{
		{name: "invalid key ID", method: http.MethodGet, path: "/logkey/invalid", wantStatus: http.StatusBadRequest},
		{name: "nil delete ID", method: http.MethodDelete, path: "/logkey/" + uuid.Nil.String(), wantStatus: http.StatusBadRequest},
		{name: "invalid account ID", method: http.MethodGet, path: "/logkey/account/invalid", wantStatus: http.StatusBadRequest},
		{name: "key not found", method: http.MethodGet, path: "/logkey/" + id, storeErr: logkeymanager.ErrKeyNotFound, wantStatus: http.StatusNotFound},
		{name: "delete not found", method: http.MethodDelete, path: "/logkey/" + id, storeErr: logkeymanager.ErrKeyNotFound, wantStatus: http.StatusNotFound},
		{name: "database error", method: http.MethodPost, path: "/logkey", body: fmt.Sprintf(`{"account_id":%q}`, testAccountID), storeErr: errors.New("private database error"), wantStatus: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := serveRequest(testRouter(&storeStub{err: tt.storeErr}, testManagementKey), tt.method, tt.path, tt.body, testManagementKey)
			if response.Code != tt.wantStatus || strings.Contains(response.Body.String(), "private database error") {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}
