package logservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrInvalidMessage = errors.New("invalid message")
	ErrInvalidCursor  = errors.New("invalid cursor")
)

type Identity struct {
	AccountID string
	KeyHash   string
}

type Log struct {
	ID        uuid.UUID
	AccountID string
	Message   string
	CreatedAt time.Time
}

type LogCursor struct {
	ID        uuid.UUID
	CreatedAt time.Time
}

type LogHandler interface {
	WriteLog(ctx context.Context, entry Log) error
	ReadLogsPaginated(ctx context.Context, identity Identity, limit int, cursor LogCursor) ([]Log, error)
	DeleteLog(ctx context.Context, logID uuid.UUID) error
	DeleteLogsByAccountID(ctx context.Context, accountID uuid.UUID) error
}

type Service struct {
	logs LogHandler
}

func New(logs LogHandler) *Service {
	return &Service{logs: logs}
}

func (s *Service) Ingest(
	ctx context.Context,
	identity Identity,
	message string,
) error {
	if !isUUID(identity.AccountID) {
		return ErrUnauthorized
	}
	if message == "" {
		return ErrInvalidMessage
	}

	err := s.logs.WriteLog(ctx, Log{
		AccountID: identity.AccountID,
		ID:        uuid.New(),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("store log: %w", err)
	}
	return nil
}

func (s *Service) PaginatedEgress(
	ctx context.Context,
	identity Identity,
	limit int,
	cursor LogCursor,
) ([]Log, bool, error) {
	if !isUUID(identity.AccountID) {
		return nil, false, ErrUnauthorized
	}
	hasCursorID := cursor.ID != uuid.Nil
	hasCursorTime := !cursor.CreatedAt.IsZero()
	if hasCursorID != hasCursorTime {
		return nil, false, ErrInvalidCursor
	}

	logs, err := s.logs.ReadLogsPaginated(ctx, identity, limit, cursor)
	if err != nil {
		return nil, false, fmt.Errorf("retrieve logs: %w", err)
	}
	next := false
	if len(logs) > limit {
		next = true
		logs = logs[:limit]
	}
	return logs, next, nil
}

func (s *Service) DeleteLogsByAccountID(
	ctx context.Context,
	accountID uuid.UUID,
) error {
	if accountID == uuid.Nil {
		return ErrInvalidMessage
	}
	err := s.logs.DeleteLogsByAccountID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("delete logs by account ID: %w", err)
	}
	return nil
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func (s *Service) DeleteLog(
	ctx context.Context,
	logID uuid.UUID,
) error {
	if logID == uuid.Nil {
		return ErrInvalidMessage
	}
	err := s.logs.DeleteLog(ctx, logID)
	if err != nil {
		return fmt.Errorf("delete log: %w", err)
	}
	return nil
}
