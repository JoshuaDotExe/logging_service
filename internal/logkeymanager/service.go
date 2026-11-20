package logkeymanager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"logging-service/internal/authoriser"

	"github.com/google/uuid"
)

var (
	ErrInvalidAccountID = errors.New("invalid account ID")
	ErrInvalidKeyID     = errors.New("invalid key ID")
	ErrInvalidScopes    = errors.New("invalid scopes")
	ErrKeyNotFound      = errors.New("log key not found")
)

type LogKey struct {
	ID              uuid.UUID  `json:"id"`
	AccountID       string     `json:"account_id"`
	Scopes          []string   `json:"scopes"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RateLimitWindow int64      `json:"rate_limit_window"`
	RateLimitMax    int64      `json:"rate_limit_max"`
	SoftDeleted     bool       `json:"soft_deleted"`
	DeactivatedAt   *time.Time `json:"deactivated_at"`
	DeactivatedBy   *string    `json:"deactivated_by"`
}

type CreatedLogKey struct {
	LogKey
	APIKey string `json:"api_key"`
}

type LogKeyStore interface {
	CreateLogKey(ctx context.Context, entry LogKey, keyHash string) (LogKey, error)
	GetLogKeysByAccountID(ctx context.Context, accountID uuid.UUID) ([]LogKey, error)
	GetLogKeyByID(ctx context.Context, id uuid.UUID) (LogKey, error)
	SoftDeleteLogKey(ctx context.Context, id uuid.UUID) error
}

type Service struct {
	store LogKeyStore
}

func NewService(store LogKeyStore) *Service {
	return &Service{store: store}
}

func (s *Service) CreateLogKey(ctx context.Context, accountID uuid.UUID, scopes []string) (CreatedLogKey, error) {
	if accountID == uuid.Nil {
		return CreatedLogKey{}, ErrInvalidAccountID
	}
	if scopes == nil {
		scopes = []string{string(authoriser.ScopeWrite)}
	}
	if len(scopes) == 0 || len(scopes) > 3 {
		return CreatedLogKey{}, ErrInvalidScopes
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if (scope != string(authoriser.ScopeWrite) &&
			scope != string(authoriser.ScopeRead) &&
			scope != string(authoriser.ScopeDelete)) || seen[scope] {
			return CreatedLogKey{}, ErrInvalidScopes
		}
		seen[scope] = true
	}

	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return CreatedLogKey{}, fmt.Errorf("generate log key: %w", err)
	}
	rawKey := "log_" + base64.RawURLEncoding.EncodeToString(bytes)
	digest := sha256.Sum256([]byte(rawKey))
	now := time.Now().UTC().Truncate(time.Second)
	entry, err := s.store.CreateLogKey(ctx, LogKey{
		AccountID:       accountID.String(),
		Scopes:          append([]string{}, scopes...),
		CreatedAt:       now,
		ExpiresAt:       now.Add(30 * 24 * time.Hour),
		RateLimitWindow: 60,
		RateLimitMax:    100,
	}, hex.EncodeToString(digest[:]))
	if err != nil {
		return CreatedLogKey{}, fmt.Errorf("create log key: %w", err)
	}
	return CreatedLogKey{LogKey: entry, APIKey: rawKey}, nil
}

func (s *Service) GetLogKeysByAccountID(ctx context.Context, accountID uuid.UUID) ([]LogKey, error) {
	if accountID == uuid.Nil {
		return nil, ErrInvalidAccountID
	}
	keys, err := s.store.GetLogKeysByAccountID(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("get account log keys: %w", err)
	}
	if keys == nil {
		keys = []LogKey{}
	}
	return keys, nil
}

func (s *Service) GetLogKeyByID(ctx context.Context, id uuid.UUID) (LogKey, error) {
	if id == uuid.Nil {
		return LogKey{}, ErrInvalidKeyID
	}
	key, err := s.store.GetLogKeyByID(ctx, id)
	if err != nil {
		return LogKey{}, fmt.Errorf("get log key: %w", err)
	}
	return key, nil
}

func (s *Service) DeleteLogKey(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return ErrInvalidKeyID
	}
	if err := s.store.SoftDeleteLogKey(ctx, id); err != nil {
		return fmt.Errorf("soft delete log key: %w", err)
	}
	return nil
}
