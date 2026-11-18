package postgresstore

import (
	"time"

	"github.com/google/uuid"
)

type LogEntry struct {
	ID        uuid.UUID
	AccountID string
	Message   string
	CreatedAt time.Time
}

type APIKeyEntry struct {
	ID              uuid.UUID
	AccountID       string
	Key             string
	Scopes          []string
	CreatedAt       time.Time
	Exp             int64
	RateLimitWindow int64
	RateLimitMax    int64
	DeactivatedAt   *time.Time
	DeactivatedBy   *string
	SoftDeleted     bool
}

type AccountEntry struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}
