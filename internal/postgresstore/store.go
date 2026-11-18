package postgresstore

import (
	"context"
	"errors"
	"fmt"

	"logging-service/internal/accountmanager"
	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ logservice.LogHandler = (*Store)(nil)
var _ authoriser.APIKeyReader = (*Store)(nil)

type Store struct {
	client *pgxpool.Pool
}

func New(client *pgxpool.Pool) *Store {
	return &Store{client: client}
}

func (s *Store) StoreAPIKey(ctx context.Context, entry *APIKeyEntry) error {
	scopes := entry.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	err := s.client.QueryRow(
		ctx,
		`INSERT INTO api_keys (account_id, key, created_at, exp, rate_limit_window, rate_limit_max, scopes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		entry.AccountID,
		entry.Key,
		entry.CreatedAt,
		entry.Exp,
		entry.RateLimitWindow,
		entry.RateLimitMax,
		scopes,
	).Scan(&entry.ID)
	if err != nil {
		return fmt.Errorf("store API key: %w", err)
	}
	return nil
}

func (s *Store) WriteLog(ctx context.Context, entry logservice.Log) error {
	return s.StoreLog(ctx, &LogEntry{
		ID:        entry.ID,
		AccountID: entry.AccountID,
		Message:   entry.Message,
		CreatedAt: entry.CreatedAt,
	})
}

func (s *Store) ReadLogsPaginated(
	ctx context.Context,
	identity logservice.Identity,
	limit int,
	cursor logservice.LogCursor,
) ([]logservice.Log, error) {
	entries, err, _ := s.GetPaginatedLogsByAccount(ctx, identity.AccountID, limit, cursor)
	if err != nil {
		return nil, err
	}

	logs := make([]logservice.Log, 0, len(entries))
	for _, entry := range entries {
		logs = append(logs, logservice.Log{
			ID:        entry.ID,
			AccountID: entry.AccountID,
			Message:   entry.Message,
			CreatedAt: entry.CreatedAt,
		})
	}
	return logs, nil
}

func (s *Store) StoreLog(ctx context.Context, entry *LogEntry) error {
	_, err := s.client.Exec(
		ctx,
		`INSERT INTO logs (id, account_id, message, created_at)
		 VALUES ($1, $2, $3, $4)`,
		entry.ID,
		entry.AccountID,
		entry.Message,
		entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("store log: %w", err)
	}
	return nil
}

func (s *Store) GetLogsByAccount(ctx context.Context, accountID string) ([]*LogEntry, error) {
	rows, err := s.client.Query(
		ctx,
		`SELECT id, account_id, message, created_at
		 FROM logs
		 WHERE account_id = $1
		 ORDER BY created_at DESC`,
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("query account logs: %w", err)
	}
	defer rows.Close()

	var logs []*LogEntry
	for rows.Next() {
		var log LogEntry
		if err := rows.Scan(&log.ID, &log.AccountID, &log.Message, &log.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account log: %w", err)
		}
		logs = append(logs, &log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account logs: %w", err)
	}
	return logs, nil
}

func (s *Store) GetPaginatedLogsByAccount(
	ctx context.Context,
	accountID string,
	limit int,
	cursor logservice.LogCursor,
) ([]*LogEntry, error, bool) {
	query := `
		SELECT id, account_id, message, created_at
		FROM logs
		WHERE account_id = $1`
	args := []any{accountID}
	if !cursor.CreatedAt.IsZero() {
		query += ` AND (created_at, id) < ($2, $3)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += `
		ORDER BY created_at DESC, id DESC
		LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)

	rows, err := s.client.Query(
		ctx,
		query,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query account logs: %w", err), false
	}
	defer rows.Close()

	var logs []*LogEntry
	for rows.Next() {
		var log LogEntry
		if err := rows.Scan(&log.ID, &log.AccountID, &log.Message, &log.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account log: %w", err), false
		}
		logs = append(logs, &log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account logs: %w", err), false
	}
	return logs, nil, len(logs) > limit
}

func (s *Store) GetAPIKeyByAPIKeyHash(ctx context.Context, apiKeyHash string) (*APIKeyEntry, error) {
	row := s.client.QueryRow(
		ctx,
		`SELECT id, account_id, key, created_at, exp, rate_limit_window, rate_limit_max,
		        deactivated_at, deactivated_by, soft_deleted, scopes
		 FROM api_keys
		 WHERE key = $1
		   AND soft_deleted = FALSE
		   AND deactivated_at IS NULL
		   AND exp > EXTRACT(EPOCH FROM statement_timestamp())`,
		apiKeyHash,
	)
	var entry APIKeyEntry
	if err := row.Scan(
		&entry.ID, &entry.AccountID, &entry.Key, &entry.CreatedAt, &entry.Exp,
		&entry.RateLimitWindow, &entry.RateLimitMax,
		&entry.DeactivatedAt, &entry.DeactivatedBy, &entry.SoftDeleted,
		&entry.Scopes,
	); err != nil {
		return nil, fmt.Errorf("get active API key: %w", err)
	}
	return &entry, nil
}

func (s *Store) GetAPIKeyCredentials(ctx context.Context, keyHash string) (authoriser.Credentials, error) {
	entry, err := s.GetAPIKeyByAPIKeyHash(ctx, keyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return authoriser.Credentials{}, authoriser.ErrInvalidAPIKey
	}
	if err != nil {
		return authoriser.Credentials{}, err
	}
	return authoriser.Credentials{AccountID: entry.AccountID, Scopes: entry.Scopes}, nil
}

func (s *Store) SoftDeleteAPIKeyByAPIKeyHash(ctx context.Context, apiKeyHash string) error {
	return s.deactivateAPIKey(ctx, `UPDATE api_keys
		 SET soft_deleted = TRUE,
		     deactivated_at = COALESCE(deactivated_at, statement_timestamp())
		 WHERE key = $1`, apiKeyHash)
}

func (s *Store) deactivateAPIKey(ctx context.Context, query string, value any) error {
	result, err := s.client.Exec(
		ctx,
		query,
		value,
	)
	if err != nil {
		return fmt.Errorf("deactivate API key: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("deactivate API key: %w", pgx.ErrNoRows)
	}
	return nil
}

func (s *Store) DeleteLogsByAccountID(ctx context.Context, accountID uuid.UUID) error {
	result, err := s.client.Exec(
		ctx,
		`DELETE FROM logs
		 WHERE account_id = $1`,
		accountID.String(),
	)
	if err != nil {
		return fmt.Errorf("delete logs by account ID: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("delete logs by account ID: %w", pgx.ErrNoRows)
	}
	return nil
}

func (s *Store) DeleteLog(ctx context.Context, logID uuid.UUID) error {
	result, err := s.client.Exec(
		ctx,
		`DELETE FROM logs
		 WHERE id = $1`,
		logID,
	)
	if err != nil {
		return fmt.Errorf("delete log: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("delete log: %w", pgx.ErrNoRows)
	}
	return nil
}

func (s *Store) CreateAccount(ctx context.Context, account accountmanager.AccountEntry) error {
	_, err := s.client.Exec(
		ctx,
		`INSERT INTO accounts (id, username, email, password_hash, password_changed_at, role, status, email_verified_at, mfa_secret_encrypted, mfa_enabled_at, failed_login_count, locked_until, last_login_at, created_at, updated_at, deleted_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		account.ID, account.Username, account.Email, account.PasswordHash, account.PasswordChangedAt,
		account.Role, account.Status, account.EmailVerifiedAt, account.MFASecretEncrypted, account.MFAEnabledAt,
		account.FailedLoginCount, account.LockedUntil, account.LastLoginAt, account.CreatedAt, account.UpdatedAt, account.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

func (s *Store) GetAccountDetails(ctx context.Context, username string) (*accountmanager.AccountDetails, error) {
	row := s.client.QueryRow(
		ctx,
		`SELECT id, username, email, email_verified_at IS NOT NULL, status, role,
		        mfa_enabled_at IS NOT NULL, created_at, updated_at, last_login_at
		 FROM accounts
		 WHERE username = $1`,
		username,
	)
	var details accountmanager.AccountDetails
	if err := row.Scan(
		&details.ID, &details.Username, &details.Email, &details.EmailVerified, &details.Status, &details.Role, &details.MFAEnabled, &details.CreatedAt, &details.UpdatedAt, &details.LastLoginAt,
	); err != nil {
		return nil, fmt.Errorf("get account details: %w", err)
	}
	return &details, nil
}

func (s *Store) DeleteAccount(ctx context.Context, username string) error {
	result, err := s.client.Exec(
		ctx,
		`DELETE FROM accounts
		 WHERE username = $1`,
		username,
	)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("delete account: %w", pgx.ErrNoRows)
	}
	return nil
}

func (s *Store) SoftDeleteAccount(ctx context.Context, username string) error {
	result, err := s.client.Exec(
		ctx,
		`UPDATE accounts
		 SET deleted_at = NOW()
		 WHERE username = $1`,
		username,
	)
	if err != nil {
		return fmt.Errorf("soft delete account: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("soft delete account: %w", pgx.ErrNoRows)
	}
	return nil
}
