package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"

	"github.com/google/uuid"
)

type authorizerStub struct {
	result authoriser.Authorization
	err    error
	apiKey string
	scope  authoriser.Scope
}

func (s *authorizerStub) Authorize(_ context.Context, apiKey string, scope authoriser.Scope) (authoriser.Authorization, error) {
	s.apiKey = apiKey
	s.scope = scope
	return s.result, s.err
}

type logStoreStub struct {
	written logservice.Log
}

func (s *logStoreStub) WriteLog(_ context.Context, entry logservice.Log) error {
	s.written = entry
	return nil
}

func (*logStoreStub) ReadLogsPaginated(context.Context, logservice.Identity, int, logservice.LogCursor) ([]logservice.Log, error) {
	return nil, nil
}

func (*logStoreStub) DeleteLog(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (*logStoreStub) DeleteLogsByAccountID(context.Context, uuid.UUID) error {
	return nil
}

func TestAuthorizationMiddlewarePassesScopeAndAccountID(t *testing.T) {
	accountID := uuid.NewString()
	authorizer := &authorizerStub{
		result: authoriser.Authorization{Authorized: true, AccountID: accountID},
	}
	store := &logStoreStub{}
	router := NewRouter(
		logservice.New(store),
		authorizer,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	request := httptest.NewRequest(http.MethodPost, "/logs", strings.NewReader(`{"message":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", "test-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	if authorizer.apiKey != "test-key" || authorizer.scope != authoriser.ScopeWrite {
		t.Fatalf("authorizer received key %q and scope %q", authorizer.apiKey, authorizer.scope)
	}
	if store.written.AccountID != accountID {
		t.Fatalf("written account ID = %q, want %q", store.written.AccountID, accountID)
	}

	request = httptest.NewRequest(http.MethodGet, "/logs", nil)
	request.Header.Set("X-API-Key", "test-key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if authorizer.scope != authoriser.ScopeNone {
		t.Fatalf("GET requested scope %q, want no specific scope", authorizer.scope)
	}
}

func TestAuthorizationMiddlewareRejectsUnavailableOrInvalidAuthorizer(t *testing.T) {
	tests := []struct {
		name       string
		authorizer *authorizerStub
		wantStatus int
	}{
		{
			name:       "authorizer unavailable",
			authorizer: &authorizerStub{err: errors.New("unavailable")},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "unauthorized",
			authorizer: &authorizerStub{},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "invalid account ID",
			authorizer: &authorizerStub{
				result: authoriser.Authorization{Authorized: true, AccountID: "not-a-uuid"},
			},
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(
				logservice.New(&logStoreStub{}),
				tt.authorizer,
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			request := httptest.NewRequest(http.MethodPost, "/logs", strings.NewReader(`{"message":"hello"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-API-Key", "test-key")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestAPIKeyCreationRouteIsNotRegistered(t *testing.T) {
	router := NewRouter(
		logservice.New(&logStoreStub{}),
		&authorizerStub{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	request := httptest.NewRequest(http.MethodPost, "/createAPIKey", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
