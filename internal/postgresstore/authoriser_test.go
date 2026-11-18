package postgresstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"logging-service/internal/authoriser"
	"logging-service/internal/authoriser/httpapi"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthoriserIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	testPool := newAPIKeyTestPool(t, ctx)
	config := testPool.Config()
	apply := func(name string) {
		t.Helper()
		applyTestMigration(t, ctx, testPool, name)
	}
	hash := func(raw string) string {
		digest := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(digest[:])
	}
	apply("000001_create_logs.up.sql")
	if _, err := testPool.Exec(ctx,
		`INSERT INTO api_keys (account_id, key, exp, rate_limit_window, rate_limit_max)
		 VALUES ($1, $2, $3, 60, 100)`,
		uuid.NewString(), hash("log_legacy"), time.Now().Add(time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}
	var legacyScopes []string
	if err := testPool.QueryRow(ctx, `SELECT scopes FROM api_keys WHERE key = $1`, hash("log_legacy")).Scan(&legacyScopes); err != nil {
		t.Fatal(err)
	}
	if legacyScopes == nil || len(legacyScopes) != 0 {
		t.Fatalf("keys without explicit scopes were granted scopes: %v", legacyScopes)
	}

	store := New(testPool)
	accountID := uuid.NewString()
	entry := APIKeyEntry{
		AccountID: accountID, Key: hash("log_active"), Scopes: []string{"read", "write"},
		CreatedAt: time.Now().UTC(), Exp: time.Now().Add(time.Hour).Unix(),
		RateLimitWindow: 60, RateLimitMax: 100,
	}
	if err := store.StoreAPIKey(ctx, &entry); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"log_expired", "log_soft_deleted", "log_deactivated"} {
		key := entry
		key.Key = hash(raw)
		if raw == "log_expired" {
			key.Exp = time.Now().Add(-time.Minute).Unix()
		}
		if err := store.StoreAPIKey(ctx, &key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE api_keys SET soft_deleted = TRUE WHERE key = $1`, hash("log_soft_deleted")); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE api_keys SET deactivated_at = now() WHERE key = $1`, hash("log_deactivated")); err != nil {
		t.Fatal(err)
	}

	readConfig := config.Copy()
	readConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readPool, err := pgxpool.NewWithConfig(ctx, readConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	var readOnly string
	if err := readPool.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if readOnly != "on" {
		t.Fatal("authoriser connection is not read-only")
	}
	service := authoriser.NewService(New(readPool))
	server := httptest.NewServer(httpapi.NewRouter(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	client, err := authoriser.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	key, err := client.VerifyAPIKey(ctx, "log_active")
	if err != nil {
		t.Fatal(err)
	}
	if key.Key != "log_active" || key.AccountID != accountID || !slices.Equal(key.Scopes, entry.Scopes) {
		t.Fatalf("incorrect API key credentials: %+v", key)
	}
	key, err = client.VerifyAPIKey(ctx, "log_legacy")
	if err != nil || key.Scopes == nil || len(key.Scopes) != 0 {
		t.Fatalf("legacy key scopes = %v, error = %v", key.Scopes, err)
	}
	for _, raw := range []string{"log_missing", "log_expired", "log_soft_deleted", "log_deactivated"} {
		if _, err := client.VerifyAPIKey(ctx, raw); !errors.Is(err, authoriser.ErrInvalidAPIKey) {
			t.Fatalf("inactive key %q returned error %v", raw, err)
		}
	}
}
