package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"logging-service/internal/authoriser"
)

type readerStub struct {
	credentials authoriser.Credentials
	err         error
	called      bool
}

func (s *readerStub) GetAPIKeyCredentials(context.Context, string) (authoriser.Credentials, error) {
	s.called = true
	return s.credentials, s.err
}

func TestVerifyAPIKeyEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantCalled bool
	}{
		{name: "valid", body: `{"api_key":"log_key"}`, wantStatus: http.StatusOK, wantCalled: true},
		{name: "malformed", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "missing", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "empty", body: `{"api_key":""}`, wantStatus: http.StatusBadRequest},
		{name: "oversized", body: `{"api_key":"` + strings.Repeat("a", 4096) + `"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown or inactive", body: `{"api_key":"log_key"}`, err: authoriser.ErrInvalidAPIKey, wantStatus: http.StatusUnauthorized, wantCalled: true},
		{name: "database failure", body: `{"api_key":"log_key"}`, err: errors.New("private database error"), wantStatus: http.StatusInternalServerError, wantCalled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &readerStub{
				credentials: authoriser.Credentials{AccountID: "account", Scopes: []string{"read", "write"}},
				err:         tt.err,
			}
			router := NewRouter(authoriser.NewService(reader), slog.New(slog.NewTextHandler(io.Discard, nil)))
			request := httptest.NewRequest(http.MethodPost, "/authorise", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantStatus || reader.called != tt.wantCalled {
				t.Fatalf("status = %d, reader called = %v; want %d, %v: %s",
					response.Code, reader.called, tt.wantStatus, tt.wantCalled, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential response is cacheable")
			}
			if response.Code == http.StatusOK {
				var key authoriser.APIKey
				if err := json.Unmarshal(response.Body.Bytes(), &key); err != nil {
					t.Fatal(err)
				}
				if key.Key != "log_key" || key.AccountID != "account" ||
					!reflect.DeepEqual(key.Scopes, []string{"read", "write"}) {
					t.Fatalf("unexpected credentials: %+v", key)
				}
			} else if strings.Contains(response.Body.String(), "log_key") ||
				strings.Contains(response.Body.String(), "private database error") {
				t.Fatal("error response leaked credentials or internal details")
			}
		})
	}
}

func TestHealthEndpoint(t *testing.T) {
	reader := &readerStub{}
	router := NewRouter(authoriser.NewService(reader), slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK || reader.called {
		t.Fatalf("health status = %d, reader called = %v", response.Code, reader.called)
	}
}
