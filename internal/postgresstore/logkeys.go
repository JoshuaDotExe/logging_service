package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logging-service/internal/logkeymanager"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ logkeymanager.LogKeyStore = (*Store)(nil)

const logKeyColumns = `id, account_id, scopes, created_at, exp, rate_limit_window,
	rate_limit_max, soft_deleted, deactivated_at, deactivated_by`

func (s *Store) CreateLogKey(ctx context.Context, key logkeymanager.LogKey, keyHash string) (logkeymanager.LogKey, error) {
	entry := APIKeyEntry{
		AccountID: key.AccountID, Key: keyHash, Scopes: key.Scopes,
		CreatedAt: key.CreatedAt, Exp: key.ExpiresAt.Unix(),
		RateLimitWindow: key.RateLimitWindow, RateLimitMax: key.RateLimitMax,
	}
	if err := s.StoreAPIKey(ctx, &entry); err != nil {
		return logkeymanager.LogKey{}, err
	}
	key.ID = entry.ID
	return key, nil
}

func (s *Store) GetLogKeysByAccountID(ctx context.Context, accountID uuid.UUID) ([]logkeymanager.LogKey, error) {
	rows, err := s.client.Query(ctx,
		`SELECT `+logKeyColumns+` FROM api_keys
		 WHERE account_id = $1 ORDER BY created_at DESC, id DESC`,
		accountID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("query account log keys: %w", err)
	}
	defer rows.Close()
	keys := []logkeymanager.LogKey{}
	for rows.Next() {
		key, err := scanLogKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account log key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account log keys: %w", err)
	}
	return keys, nil
}

func (s *Store) GetLogKeyByID(ctx context.Context, id uuid.UUID) (logkeymanager.LogKey, error) {
	key, err := scanLogKey(s.client.QueryRow(ctx,
		`SELECT `+logKeyColumns+` FROM api_keys WHERE id = $1`, id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return logkeymanager.LogKey{}, logkeymanager.ErrKeyNotFound
	}
	if err != nil {
		return logkeymanager.LogKey{}, fmt.Errorf("get log key metadata: %w", err)
	}
	return key, nil
}

func (s *Store) SoftDeleteLogKey(ctx context.Context, id uuid.UUID) error {
	err := s.deactivateAPIKey(ctx, `UPDATE api_keys
		 SET soft_deleted = TRUE,
		     deactivated_at = COALESCE(deactivated_at, statement_timestamp())
		 WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return logkeymanager.ErrKeyNotFound
	}
	return err
}

func scanLogKey(row pgx.Row) (logkeymanager.LogKey, error) {
	var key logkeymanager.LogKey
	var expiry int64
	if err := row.Scan(&key.ID, &key.AccountID, &key.Scopes, &key.CreatedAt, &expiry,
		&key.RateLimitWindow, &key.RateLimitMax, &key.SoftDeleted, &key.DeactivatedAt, &key.DeactivatedBy); err != nil {
		return logkeymanager.LogKey{}, err
	}
	key.ExpiresAt = time.Unix(expiry, 0).UTC()
	return key, nil
}
