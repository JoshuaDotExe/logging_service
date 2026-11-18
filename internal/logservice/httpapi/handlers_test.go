package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"logging-service/internal/logservice"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type deletionStoreStub struct {
	deletedAccount uuid.UUID
	called         bool
	err            error
}

func (*deletionStoreStub) WriteLog(context.Context, logservice.Log) error {
	return nil
}

func (*deletionStoreStub) ReadLogsPaginated(context.Context, logservice.Identity, int, logservice.LogCursor) ([]logservice.Log, error) {
	return nil, nil
}

func (*deletionStoreStub) DeleteLog(context.Context, uuid.UUID) error {
	return nil
}

func (s *deletionStoreStub) DeleteLogsByAccountID(_ context.Context, accountID uuid.UUID) error {
	s.called = true
	s.deletedAccount = accountID
	return s.err
}

func TestDeleteLogsByAccountID(t *testing.T) {
	accountID := uuid.New()
	for _, tt := range []struct {
		name        string
		identity    interface{}
		query       string
		storeErr    error
		wantStatus  int
		wantAccount uuid.UUID
		wantCalled  bool
	}{
		{
			name:       "authenticated account without query",
			identity:   logservice.Identity{AccountID: accountID.String()},
			wantStatus: http.StatusNoContent, wantAccount: accountID, wantCalled: true,
		},
		{
			name:       "query cannot override authenticated account",
			identity:   logservice.Identity{AccountID: accountID.String()},
			query:      "?account_id=" + uuid.NewString(),
			wantStatus: http.StatusNoContent, wantAccount: accountID, wantCalled: true,
		},
		{
			name:       "missing identity",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "invalid identity type", identity: "not-an-identity",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "store error", identity: logservice.Identity{AccountID: accountID.String()},
			storeErr:   errors.New("delete failed"),
			wantStatus: http.StatusInternalServerError, wantAccount: accountID, wantCalled: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &deletionStoreStub{err: tt.storeErr}
			handlers := &Handlers{
				service: logservice.New(store),
				logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			router := gin.New()
			router.DELETE("/logs", func(c *gin.Context) {
				if tt.identity != nil {
					c.Set(identityContextKey, tt.identity)
				}
				c.Next()
			}, handlers.deleteLogsByAccountID)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/logs"+tt.query, nil))

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if store.called != tt.wantCalled || store.deletedAccount != tt.wantAccount {
				t.Fatalf("delete called = %v, account = %q; want %v, %q",
					store.called, store.deletedAccount, tt.wantCalled, tt.wantAccount)
			}
		})
	}
}
