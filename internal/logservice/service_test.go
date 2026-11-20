package logservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type logHandlerStub struct {
	writeCalled bool
}

func (s *logHandlerStub) WriteLog(context.Context, Log) error {
	s.writeCalled = true
	return nil
}

func (*logHandlerStub) ReadLogsPaginated(context.Context, Identity, int, LogCursor) ([]Log, error) {
	return nil, nil
}

func (*logHandlerStub) DeleteLog(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (*logHandlerStub) DeleteLogsByAccountID(context.Context, uuid.UUID) error {
	return nil
}

func TestIngestRejectsNonUUIDAccountID(t *testing.T) {
	logs := &logHandlerStub{}
	service := New(logs)

	err := service.Ingest(context.Background(), Identity{AccountID: "account-123"}, "message")
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if logs.writeCalled {
		t.Fatal("log stored for an identity with an invalid account ID")
	}
}

func TestPaginatedEgressRejectsNonUUIDAccountID(t *testing.T) {
	service := New(&logHandlerStub{})

	_, _, err := service.PaginatedEgress(
		context.Background(),
		Identity{AccountID: "account-123"},
		10,
		LogCursor{CreatedAt: time.Now()},
	)
	if err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}
