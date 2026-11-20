package authoriser_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"logging-service/internal/authoriser"
	authoriserapi "logging-service/internal/authoriser/httpapi"
	"logging-service/internal/logservice"
	logapi "logging-service/internal/logservice/httpapi"

	"github.com/google/uuid"
)

type credentialReaderStub struct {
	credentials authoriser.Credentials
	err         error
}

func (s credentialReaderStub) GetAPIKeyCredentials(context.Context, string) (authoriser.Credentials, error) {
	return s.credentials, s.err
}

type logStoreStub struct {
	accountID string
}

func (s *logStoreStub) WriteLog(_ context.Context, entry logservice.Log) error {
	s.accountID = entry.AccountID
	return nil
}

func (s *logStoreStub) ReadLogsPaginated(_ context.Context, identity logservice.Identity, _ int, _ logservice.LogCursor) ([]logservice.Log, error) {
	s.accountID = identity.AccountID
	return nil, nil
}

func (*logStoreStub) DeleteLog(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (*logStoreStub) DeleteLogsByAccountID(context.Context, uuid.UUID) error {
	return nil
}

func TestLogserviceUsesAuthoriserHTTPService(t *testing.T) {
	accountID := uuid.NewString()
	for _, tt := range []struct {
		name       string
		scopes     []string
		readerErr  error
		method     string
		wantStatus int
	}{
		{name: "write permitted", scopes: []string{"read", "write"}, method: http.MethodPost, wantStatus: http.StatusCreated},
		{name: "write denied", scopes: []string{"read"}, method: http.MethodPost, wantStatus: http.StatusUnauthorized},
		{name: "empty scopes cannot write", scopes: []string{}, method: http.MethodPost, wantStatus: http.StatusUnauthorized},
		{name: "read permitted", scopes: []string{"read"}, method: http.MethodGet, wantStatus: http.StatusOK},
		{name: "empty scopes cannot read", scopes: []string{}, method: http.MethodGet, wantStatus: http.StatusUnauthorized},
		{name: "unknown key", readerErr: authoriser.ErrInvalidAPIKey, method: http.MethodPost, wantStatus: http.StatusUnauthorized},
		{name: "database unavailable", readerErr: errors.New("database unavailable"), method: http.MethodPost, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			reader := credentialReaderStub{
				credentials: authoriser.Credentials{AccountID: accountID, Scopes: tt.scopes},
				err:         tt.readerErr,
			}
			server := httptest.NewServer(authoriserapi.NewRouter(authoriser.NewService(reader), logger))
			defer server.Close()
			client, err := authoriser.NewClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}

			if tt.readerErr == nil {
				key, err := client.VerifyAPIKey(t.Context(), "log_test")
				if err != nil || key.Key != "log_test" || key.AccountID != accountID ||
					!slices.Equal(key.Scopes, tt.scopes) {
					t.Fatalf("authoriser verification = %+v, error = %v", key, err)
				}
			}

			store := &logStoreStub{}
			router := logapi.NewRouter(logservice.New(store), client, logger)
			request := httptest.NewRequest(tt.method, "/logs", strings.NewReader(`{"message":"hello","account_id":"attacker"}`))
			request.Header.Set("X-API-Key", "log_test")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if response.Code < 300 {
				if store.accountID != accountID {
					t.Fatalf("log operation used account %q, want %q", store.accountID, accountID)
				}
			} else if store.accountID != "" {
				t.Fatal("rejected request reached the log store")
			}
		})
	}
}

func TestClientRejectsInvalidResponses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusInternalServerError},
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "malformed", status: http.StatusOK, body: `{`},
		{name: "missing key", status: http.StatusOK, body: `{"account_id":"account","scopes":[]}`},
		{name: "different key", status: http.StatusOK, body: `{"api_key":"other","account_id":"account","scopes":[]}`},
		{name: "missing scopes", status: http.StatusOK, body: `{"api_key":"log_test","account_id":"account"}`},
		{name: "missing account", status: http.StatusOK, body: `{"api_key":"log_test","scopes":[]}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/authorise" {
					t.Errorf("unexpected authoriser request path %q", r.URL.Path)
				}
				if tt.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/other")
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client, err := authoriser.NewClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.VerifyAPIKey(t.Context(), "log_test"); err == nil {
				t.Fatal("client accepted an invalid response")
			}
			if calls != 1 {
				t.Fatalf("client sent %d requests; redirects must not replay credentials", calls)
			}
		})
	}
}

func TestClientPreservesCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("canceled verification reached the server")
	}))
	defer server.Close()
	client, err := authoriser.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.VerifyAPIKey(ctx, "log_test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestNewClientRequiresValidURL(t *testing.T) {
	for _, baseURL := range []string{"", "authoriser", "ftp://authoriser", "http://user:password@authoriser", "http://authoriser?key=value", "http://authoriser#fragment"} {
		if _, err := authoriser.NewClient(baseURL); err == nil {
			t.Fatalf("accepted invalid URL %q", baseURL)
		}
	}
}
