package accountmanager

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type accountStoreStub struct {
	created AccountEntry
}

func (s *accountStoreStub) CreateAccount(_ context.Context, account AccountEntry) error {
	s.created = account
	return nil
}

func (*accountStoreStub) GetAccountDetails(context.Context, string) (*AccountDetails, error) {
	return nil, nil
}

func (*accountStoreStub) DeleteAccount(context.Context, uuid.UUID) error {
	return nil
}

func (*accountStoreStub) SoftDeleteAccount(context.Context, uuid.UUID) error {
	return nil
}

func (*accountStoreStub) SoftDeleteAccountByID(context.Context, uuid.UUID) error {
	return nil
}

func TestCreateAccountActivatesAccountWithoutEmailVerification(t *testing.T) {
	store := &accountStoreStub{}
	service := NewService(store)

	if err := service.CreateAccount("example", "example@example.test", "correct-long-password"); err != nil {
		t.Fatal(err)
	}
	if store.created.Status != "active" {
		t.Fatalf("new account status = %q, want active", store.created.Status)
	}
	if store.created.EmailVerifiedAt != nil {
		t.Fatal("new account unexpectedly requires email verification")
	}
}
