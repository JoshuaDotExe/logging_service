package postgresstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGetAccountDetailsIntegration(t *testing.T) {
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
	schemaName := fmt.Sprintf("account_details_test_%d", time.Now().UnixNano())
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
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000001_create_logs.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO accounts (username, email, password_hash)
		VALUES ('test-user', 'test@example.test', 'hash')`); err != nil {
		t.Fatal(err)
	}
	store := New(testPool)
	for _, tc := range []struct {
		name          string
		emailVerified bool
		mfaEnabled    bool
	}{
		{"neither", false, false},
		{"email only", true, false},
		{"mfa only", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testPool.Exec(ctx, `UPDATE accounts SET
				email_verified_at = CASE WHEN $1 THEN now() ELSE NULL END,
				mfa_enabled_at = CASE WHEN $2 THEN now() ELSE NULL END,
				mfa_secret_encrypted = CASE WHEN $2 THEN decode('01', 'hex') ELSE NULL END
				WHERE username = 'test-user'`, tc.emailVerified, tc.mfaEnabled)
			if err != nil {
				t.Fatal(err)
			}
			details, err := store.GetAccountDetails(ctx, "test-user")
			if err != nil {
				t.Fatal(err)
			}
			if details.Username != "test-user" || details.EmailVerified != tc.emailVerified || details.MFAEnabled != tc.mfaEnabled {
				t.Fatalf("unexpected account details: %+v", details)
			}
		})
	}
}
