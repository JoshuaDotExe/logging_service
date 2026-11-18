package accountmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logging-service/internal/tokenhandler"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials or account unavailable")

type LoginStore interface {
	GetLoginAccount(context.Context, string) (AccountEntry, error)
	RecordLoginFailure(context.Context, uuid.UUID) error
	CompleteLogin(context.Context, uuid.UUID, string) error
	GetAccountDetailsByID(context.Context, uuid.UUID) (*AccountDetails, error)
	SoftDeleteAccountByID(context.Context, uuid.UUID) error
}

type TokenIssuer interface {
	Issue(context.Context, uuid.UUID) (tokenhandler.Token, error)
}

type LoginService struct {
	store     LoginStore
	issuer    TokenIssuer
	dummyHash []byte
}

func NewLoginService(store LoginStore, issuer TokenIssuer) (*LoginService, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("initialize login password check: %w", err)
	}
	return &LoginService{store: store, issuer: issuer, dummyHash: hash}, nil
}

func (s *LoginService) Login(ctx context.Context, username, password string) (tokenhandler.Token, error) {
	if username == "" || len(password) == 0 || len(password) > 72 {
		return tokenhandler.Token{}, ErrInvalidCredentials
	}
	account, err := s.store.GetLoginAccount(ctx, username)
	if errors.Is(err, ErrInvalidCredentials) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return tokenhandler.Token{}, ErrInvalidCredentials
	}
	if err != nil {
		return tokenhandler.Token{}, fmt.Errorf("load login account: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
		if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return tokenhandler.Token{}, fmt.Errorf("check password hash: %w", err)
		}
		if err := s.store.RecordLoginFailure(ctx, account.ID); err != nil {
			return tokenhandler.Token{}, fmt.Errorf("record failed login: %w", err)
		}
		return tokenhandler.Token{}, ErrInvalidCredentials
	}
	if account.Status != "active" || account.DeletedAt != nil || account.MFAEnabledAt != nil ||
		(account.LockedUntil != nil && account.LockedUntil.After(time.Now())) {
		return tokenhandler.Token{}, ErrInvalidCredentials
	}
	if err := s.store.CompleteLogin(ctx, account.ID, account.PasswordHash); err != nil {
		return tokenhandler.Token{}, fmt.Errorf("complete login: %w", err)
	}
	token, err := s.issuer.Issue(ctx, account.ID)
	if err != nil {
		return tokenhandler.Token{}, fmt.Errorf("issue login token: %w", err)
	}
	return token, nil
}
