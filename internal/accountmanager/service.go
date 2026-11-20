package accountmanager

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidPassword = fmt.Errorf("invalid password")
)

type AccountCreate struct {
	Username     string
	Email        string
	PasswordHash string
}

type AccountDetails struct {
	ID            uuid.UUID
	Username      string
	Email         string
	EmailVerified bool
	Status        string
	Role          string
	MFAEnabled    bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastLoginAt   *time.Time
}

type AccountEntry struct {
	ID       uuid.UUID
	Username string
	Email    string

	PasswordHash      string
	PasswordChangedAt time.Time

	Role   string // owner | admin | member | viewer
	Status string // pending_verification | active | suspended

	EmailVerifiedAt *time.Time

	MFASecretEncrypted []byte
	MFAEnabledAt       *time.Time

	FailedLoginCount int32
	LockedUntil      *time.Time
	LastLoginAt      *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time // soft delete
}

type AccountStore interface {
	CreateAccount(ctx context.Context, account AccountEntry) error
	GetAccountDetails(ctx context.Context, username string) (*AccountDetails, error)
	DeleteAccount(ctx context.Context, id uuid.UUID) error
	SoftDeleteAccount(ctx context.Context, id uuid.UUID) error
	SoftDeleteAccountByID(ctx context.Context, id uuid.UUID) error
}

type Service struct {
	handler AccountStore
}

func NewService(handler AccountStore) *Service {
	return &Service{handler: handler}
}

func (s *Service) CreateAccount(username, email, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return ErrInvalidPassword
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	account := AccountEntry{
		ID:                uuid.New(),
		Username:          username,
		Email:             email,
		PasswordHash:      string(passwordHash),
		PasswordChangedAt: time.Now(),
		Role:              "member",
		Status:            "pending_verification",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	return s.handler.CreateAccount(context.Background(), account)
}

func (s *Service) DeleteAccount(id uuid.UUID) error {
	if id == uuid.Nil {
		return fmt.Errorf("invalid account ID")
	}
	return s.handler.SoftDeleteAccountByID(context.Background(), id)
}

func (s *Service) GetAccountDetails(username string) (*AccountDetails, error) {
	return s.handler.GetAccountDetails(context.Background(), username)
}
