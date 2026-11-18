package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"logging-service/internal/logkeymanager"
	"logging-service/internal/logservice"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := pgxpool.New(context.Background(), "postgres://localhost:1/test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := New(pool)

	tests := map[string]func() error{
		"store key": func() error { return store.StoreAPIKey(ctx, &APIKeyEntry{}) },
		"store log": func() error { return store.StoreLog(ctx, &LogEntry{}) },
		"write log": func() error { return store.WriteLog(ctx, logservice.Log{}) },
		"get logs": func() error {
			_, err := store.GetLogsByAccount(ctx, "account")
			return err
		},
		"get key": func() error {
			_, err := store.GetAPIKeyByAPIKeyHash(ctx, "hash")
			return err
		},
		"get credentials": func() error {
			_, err := store.GetAPIKeyCredentials(ctx, "hash")
			return err
		},
		"deactivate key": func() error { return store.SoftDeleteAPIKeyByAPIKeyHash(ctx, "hash") },
		"create managed key": func() error {
			_, err := store.CreateLogKey(ctx, logkeymanager.LogKey{}, "hash")
			return err
		},
		"get managed key": func() error {
			_, err := store.GetLogKeyByID(ctx, uuid.New())
			return err
		},
		"list managed keys": func() error {
			_, err := store.GetLogKeysByAccountID(ctx, uuid.New())
			return err
		},
		"soft delete managed key": func() error { return store.SoftDeleteLogKey(ctx, uuid.New()) },
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context cancellation, got %v", err)
			}
		})
	}
}

func TestStoreIntegration(t *testing.T) {
	url := os.Getenv("POSTGRESSTORE_TEST_URL")
	if url == "" {
		t.Skip("set POSTGRESSTORE_TEST_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schemaName := fmt.Sprintf("postgresstore_test_%d", time.Now().UnixNano())
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean up test schema: %v", err)
		}
	}()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	testPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer testPool.Close()
	apply := func(name string) {
		t.Helper()
		sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	apply("000001_create_logs.up.sql")
	apply("000001_create_logs.down.sql")
	var remainingTables int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = $1 AND table_name IN ('logs', 'api_keys')`,
		schemaName,
	).Scan(&remainingTables); err != nil {
		t.Fatal(err)
	}
	if remainingTables != 0 {
		t.Fatal("down migration left application tables behind")
	}
	apply("000001_create_logs.up.sql")
	store := New(testPool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	hash := "hash' OR TRUE --"
	key := APIKeyEntry{
		AccountID: "account", Key: hash, CreatedAt: now,
		Scopes: []string{"write", "read"},
		Exp:    now.Add(time.Hour).Unix(), RateLimitWindow: 60, RateLimitMax: 100,
	}
	if err := store.StoreAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAPIKeyByAPIKeyHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == uuid.Nil || got.AccountID != key.AccountID || got.Key != hash ||
		!got.CreatedAt.Equal(now) || got.Exp != key.Exp ||
		got.RateLimitWindow != 60 || got.RateLimitMax != 100 ||
		got.DeactivatedAt != nil || got.DeactivatedBy != nil || got.SoftDeleted {
		t.Fatalf("unexpected active key: %+v", got)
	}
	if !slices.Equal(got.Scopes, key.Scopes) {
		t.Fatalf("key scopes = %v, want %v", got.Scopes, key.Scopes)
	}
	assertNotFound := func(hash string) {
		t.Helper()
		entry, err := store.GetAPIKeyByAPIKeyHash(ctx, hash)
		if entry != nil || !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expected no active key, got entry=%+v err=%v", entry, err)
		}
	}
	assertNotFound("missing")
	key.Key = "expired"
	key.Exp = now.Add(-time.Second).Unix()
	if err := store.StoreAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	assertNotFound("expired")
	for _, hash := range []string{"soft-deleted", "timestamp-deactivated"} {
		key.Key, key.Exp = hash, now.Add(time.Hour).Unix()
		if err := store.StoreAPIKey(ctx, &key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE api_keys SET soft_deleted = TRUE WHERE key = 'soft-deleted'`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE api_keys SET deactivated_at = now() WHERE key = 'timestamp-deactivated'`); err != nil {
		t.Fatal(err)
	}
	assertNotFound("soft-deleted")
	assertNotFound("timestamp-deactivated")
	if err := store.SoftDeleteAPIKeyByAPIKeyHash(ctx, "missing"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected missing deactivation error, got %v", err)
	}
	if err := store.SoftDeleteAPIKeyByAPIKeyHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	var first, second time.Time
	if err := testPool.QueryRow(ctx, `SELECT deactivated_at FROM api_keys WHERE key = $1`, hash).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeleteAPIKeyByAPIKeyHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT deactivated_at FROM api_keys WHERE key = $1`, hash).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) {
		t.Fatal("repeated deactivation changed the original timestamp")
	}
	assertNotFound(hash)

	if err := store.StoreLog(ctx, &LogEntry{
		ID: uuid.New(), AccountID: "other", Message: "hidden", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.WriteLog(ctx, logservice.Log{
			ID: uuid.New(), AccountID: "account", Message: fmt.Sprintf("log %d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := store.GetLogsByAccount(ctx, "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Message != "log 1" || logs[1].Message != "log 0" ||
		logs[0].ID == uuid.Nil || logs[1].ID == uuid.Nil || logs[0].ID == logs[1].ID {
		t.Fatalf("unexpected account logs: %+v", logs)
	}
}
