package accountmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"logging-service/internal/tokenhandler"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type loginStub struct {
	account     AccountEntry
	loadErr     error
	completeErr error
	issueErr    error
	failures    int
	issued      int
}

func (s *loginStub) GetLoginAccount(context.Context, string) (AccountEntry, error) {
	return s.account, s.loadErr
}
func (s *loginStub) RecordLoginFailure(context.Context, uuid.UUID) error {
	s.failures++
	return nil
}
func (s *loginStub) CompleteLogin(context.Context, uuid.UUID, string) error { return s.completeErr }
func (s *loginStub) GetAccountDetailsByID(context.Context, uuid.UUID) (*AccountDetails, error) {
	return &AccountDetails{ID: s.account.ID}, nil
}
func (s *loginStub) SoftDeleteAccountByID(context.Context, uuid.UUID) error { return nil }
func (s *loginStub) Issue(_ context.Context, id uuid.UUID) (tokenhandler.Token, error) {
	s.issued++
	if id != s.account.ID {
		return tokenhandler.Token{}, errors.New("wrong token subject")
	}
	return tokenhandler.Token{AccessToken: "test-token", TokenType: "Bearer", ExpiresIn: 900}, s.issueErr
}

func TestLoginRequiresCredentialsAndEligibility(t *testing.T) {
	const password = "correct-long-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	for _, name := range []string{"valid", "wrong password", "missing account", "pending", "suspended", "deleted", "locked", "mfa", "database error", "concurrent suspension", "issuer unavailable"} {
		t.Run(name, func(t *testing.T) {
			stub := &loginStub{account: AccountEntry{ID: uuid.New(), Status: "active", PasswordHash: string(hash)}}
			input := password
			switch name {
			case "wrong password":
				input = "incorrect"
			case "missing account":
				stub.loadErr = ErrInvalidCredentials
			case "pending":
				stub.account.Status = "pending_verification"
			case "suspended":
				stub.account.Status = "suspended"
			case "deleted":
				stub.account.DeletedAt = &future
			case "locked":
				stub.account.LockedUntil = &future
			case "mfa":
				stub.account.MFAEnabledAt = &future
			case "database error":
				stub.loadErr = errors.New("database unavailable")
			case "concurrent suspension":
				stub.completeErr = ErrInvalidCredentials
			case "issuer unavailable":
				stub.issueErr = errors.New("token service unavailable")
			}
			service, err := NewLoginService(stub, stub)
			if err != nil {
				t.Fatal(err)
			}
			token, err := service.Login(context.Background(), "owner", input)
			if name == "valid" {
				if err != nil || token.AccessToken == "" || stub.issued != 1 {
					t.Fatalf("login failed: %v", err)
				}
			} else {
				if err == nil || token.AccessToken != "" {
					t.Fatal("invalid login returned a token")
				}
				if name != "issuer unavailable" && stub.issued != 0 {
					t.Fatal("ineligible login requested token issuance")
				}
			}
			if name == "wrong password" && stub.failures != 1 {
				t.Fatal("failed password was not recorded")
			}
		})
	}
}
