package postgresstore

import (
	"context"
	"errors"
	"fmt"

	"logging-service/internal/accountmanager"
	"logging-service/internal/tokenhandler"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) GetActiveAccount(ctx context.Context, id uuid.UUID) (tokenhandler.Account, error) {
	var account tokenhandler.Account
	err := s.client.QueryRow(ctx, `SELECT id, username, role FROM accounts
		WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		AND (locked_until IS NULL OR locked_until <= now())`, id).
		Scan(&account.ID, &account.Username, &account.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return tokenhandler.Account{}, tokenhandler.ErrAccountIneligible
	}
	if err != nil {
		return tokenhandler.Account{}, fmt.Errorf("get active account: %w", err)
	}
	return account, nil
}

func (s *Store) GetLoginAccount(ctx context.Context, username string) (accountmanager.AccountEntry, error) {
	var a accountmanager.AccountEntry
	err := s.client.QueryRow(ctx, `SELECT id, password_hash, status, locked_until, mfa_enabled_at
		FROM accounts WHERE lower(username) = lower($1) AND deleted_at IS NULL`, username).
		Scan(&a.ID, &a.PasswordHash, &a.Status, &a.LockedUntil, &a.MFAEnabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, accountmanager.ErrInvalidCredentials
	}
	if err != nil {
		return a, fmt.Errorf("get login account: %w", err)
	}
	return a, nil
}

func (s *Store) RecordLoginFailure(ctx context.Context, id uuid.UUID) error {
	_, err := s.client.Exec(ctx, `UPDATE accounts SET
		failed_login_count = CASE WHEN locked_until <= now() THEN 1 ELSE failed_login_count + 1 END,
		locked_until = CASE
			WHEN locked_until > now() THEN locked_until
			WHEN locked_until <= now() THEN NULL
			WHEN failed_login_count + 1 >= 5 THEN now() + interval '15 minutes'
			ELSE NULL END,
		updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("record failed login: %w", err)
	}
	return nil
}

func (s *Store) CompleteLogin(ctx context.Context, id uuid.UUID, passwordHash string) error {
	result, err := s.client.Exec(ctx, `UPDATE accounts SET failed_login_count = 0,
		locked_until = NULL, last_login_at = now(), updated_at = now()
		WHERE id = $1 AND password_hash = $2 AND status = 'active' AND deleted_at IS NULL
		AND mfa_enabled_at IS NULL AND (locked_until IS NULL OR locked_until <= now())`, id, passwordHash)
	if err != nil {
		return fmt.Errorf("update successful login: %w", err)
	}
	if result.RowsAffected() == 0 {
		return accountmanager.ErrInvalidCredentials
	}
	return nil
}

func (s *Store) GetAccountDetailsByID(ctx context.Context, id uuid.UUID) (*accountmanager.AccountDetails, error) {
	var a accountmanager.AccountDetails
	err := s.client.QueryRow(ctx, `SELECT id, username, email, email_verified_at IS NOT NULL,
		status, role, mfa_enabled_at IS NOT NULL, created_at, updated_at, last_login_at
		FROM accounts WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&a.ID, &a.Username, &a.Email, &a.EmailVerified, &a.Status, &a.Role,
			&a.MFAEnabled, &a.CreatedAt, &a.UpdatedAt, &a.LastLoginAt)
	if err != nil {
		return nil, fmt.Errorf("get own account: %w", err)
	}
	return &a, nil
}

func (s *Store) SoftDeleteAccountByID(ctx context.Context, id uuid.UUID) error {
	result, err := s.client.Exec(ctx, `UPDATE accounts SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete own account: %w", err)
	}
	if result.RowsAffected() == 0 {
		return tokenhandler.ErrAccountIneligible
	}
	return nil
}
