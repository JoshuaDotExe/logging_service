package postgresstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"logging-service/internal/authoriser"
	authoriserapi "logging-service/internal/authoriser/httpapi"
	"logging-service/internal/logkeymanager"
	keyapi "logging-service/internal/logkeymanager/httpapi"
	"logging-service/internal/logservice"
	logapi "logging-service/internal/logservice/httpapi"

	"github.com/google/uuid"
)

func TestLogKeyManagerIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := newAPIKeyTestPool(t, ctx)
	applyTestMigration(t, ctx, pool, "000001_create_logs.up.sql")
	store := New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, tokenVerifier := newAccountTokenKeys(t)
	var currentAccount uuid.UUID
	useAccount := func(id uuid.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, username, email, password_hash, status)
			VALUES ($1, $2, $3, 'unused', 'active') ON CONFLICT (id) DO NOTHING`,
			id, id.String(), id.String()+"@example.test"); err != nil {
			t.Fatal(err)
		}
		currentAccount = id
	}
	manager := httptest.NewServer(keyapi.NewRouter(logkeymanager.NewService(store), logger, tokenVerifier, store))
	defer manager.Close()
	verifier := httptest.NewServer(authoriserapi.NewRouter(authoriser.NewService(store), logger))
	defer verifier.Close()
	client, err := authoriser.NewClient(verifier.URL)
	if err != nil {
		t.Fatal(err)
	}
	logs := httptest.NewServer(logapi.NewRouter(logservice.New(store), client, logger))
	defer logs.Close()

	request := func(method, url, body, header, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(header, token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	manage := func(method, path, body string) (int, []byte) {
		t.Helper()
		token, err := signer.Issue(currentAccount)
		if err != nil {
			t.Fatal(err)
		}
		return request(method, manager.URL+path, body, "Authorization", "Bearer "+token.AccessToken)
	}
	create := func(accountID uuid.UUID, scopes []string) logkeymanager.CreatedLogKey {
		t.Helper()
		useAccount(accountID)
		body := map[string]any{"account_id": accountID.String()}
		if scopes != nil {
			body["scopes"] = scopes
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		status, data := manage(http.MethodPost, "/logkey", string(data))
		if status != http.StatusCreated {
			t.Fatalf("create status = %d: %s", status, data)
		}
		var key logkeymanager.CreatedLogKey
		if err := json.Unmarshal(data, &key); err != nil {
			t.Fatal(err)
		}
		if key.ID == uuid.Nil || key.AccountID != accountID.String() || key.APIKey == "" ||
			!key.ExpiresAt.Equal(key.CreatedAt.Add(30*24*time.Hour)) {
			t.Fatalf("invalid created key metadata: %+v", key.LogKey)
		}
		digest := sha256.Sum256([]byte(key.APIKey))
		var storedHash string
		if err := pool.QueryRow(ctx, `SELECT key FROM api_keys WHERE id = $1`, key.ID).Scan(&storedHash); err != nil {
			t.Fatal(err)
		}
		if storedHash != hex.EncodeToString(digest[:]) || storedHash == key.APIKey {
			t.Fatal("raw key was stored instead of its SHA-256 hash")
		}
		return key
	}
	get := func(id uuid.UUID) logkeymanager.LogKey {
		t.Helper()
		owner, err := store.GetLogKeyByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		useAccount(uuid.MustParse(owner.AccountID))
		status, data := manage(http.MethodGet, "/logkey/"+id.String(), "")
		if status != http.StatusOK {
			t.Fatalf("get status = %d: %s", status, data)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["api_key"] != nil || fields["key"] != nil || fields["key_hash"] != nil {
			t.Fatal("metadata lookup returned credential material")
		}
		var key logkeymanager.LogKey
		if err := json.Unmarshal(data, &key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	list := func(accountID uuid.UUID) []logkeymanager.LogKey {
		t.Helper()
		useAccount(accountID)
		status, data := manage(http.MethodGet, "/logkey/account/"+accountID.String(), "")
		if status != http.StatusOK {
			t.Fatalf("list status = %d: %s", status, data)
		}
		var result struct {
			Keys []logkeymanager.LogKey `json:"keys"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Keys == nil {
			t.Fatal("list returned null instead of an array")
		}
		return result.Keys
	}
	write := func(key string) int {
		t.Helper()
		status, _ := request(http.MethodPost, logs.URL+"/logs",
			`{"message":"keymanager integration","account_id":"attacker"}`, "X-API-Key", key)
		return status
	}

	accountA, accountB := uuid.New(), uuid.New()
	defaultKey := create(accountA, nil)
	readKey := create(accountA, []string{"read"})
	bothKey := create(accountB, []string{"read", "write"})
	if !slices.Equal(defaultKey.Scopes, []string{"write"}) || !slices.Equal(readKey.Scopes, []string{"read"}) ||
		!slices.Equal(bothKey.Scopes, []string{"read", "write"}) {
		t.Fatal("key scopes were not persisted correctly")
	}
	if keys := list(accountA); len(keys) != 2 {
		t.Fatalf("account A has %d keys, want 2", len(keys))
	}
	if keys := list(uuid.New()); len(keys) != 0 {
		t.Fatal("empty account returned unrelated keys")
	}
	active := get(defaultKey.ID)
	if active.ID != defaultKey.ID || active.SoftDeleted || active.DeactivatedAt != nil ||
		!active.ExpiresAt.Equal(defaultKey.ExpiresAt) {
		t.Fatalf("unexpected active metadata: %+v", active)
	}
	verified, err := client.VerifyAPIKey(ctx, defaultKey.APIKey)
	if err != nil || verified.AccountID != accountA.String() || !slices.Equal(verified.Scopes, []string{"write"}) {
		t.Fatalf("authoriser credentials = %+v, error = %v", verified, err)
	}
	if status := write(defaultKey.APIKey); status != http.StatusCreated {
		t.Fatalf("default write key status = %d", status)
	}
	if status := write(readKey.APIKey); status != http.StatusUnauthorized {
		t.Fatalf("read-only key was able to write: %d", status)
	}
	if status := write(bothKey.APIKey); status != http.StatusCreated {
		t.Fatalf("both-scoped key status = %d", status)
	}
	status, data := manage(http.MethodDelete, "/logkey/"+defaultKey.ID.String(), "")
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", status, data)
	}
	deleted := get(defaultKey.ID)
	if !deleted.SoftDeleted || deleted.DeactivatedAt == nil {
		t.Fatal("soft deletion did not preserve the row and record deactivation")
	}
	status, _ = manage(http.MethodDelete, "/logkey/"+defaultKey.ID.String(), "")
	repeated := get(defaultKey.ID)
	if status != http.StatusNoContent || repeated.DeactivatedAt == nil ||
		!repeated.DeactivatedAt.Equal(*deleted.DeactivatedAt) {
		t.Fatal("repeated soft deletion was not idempotent")
	}
	if _, err := client.VerifyAPIKey(ctx, defaultKey.APIKey); !errors.Is(err, authoriser.ErrInvalidAPIKey) {
		t.Fatalf("soft-deleted key is still valid: %v", err)
	}
	if status := write(defaultKey.APIKey); status != http.StatusUnauthorized {
		t.Fatalf("soft-deleted key was able to write: %d", status)
	}
	keys := list(accountA)
	if len(keys) != 2 || (!keys[0].SoftDeleted && !keys[1].SoftDeleted) {
		t.Fatal("account key listing lost the soft-deleted record")
	}
	storedLogs, err := store.GetLogsByAccount(ctx, accountA.String())
	if err != nil || len(storedLogs) != 1 || storedLogs[0].AccountID != accountA.String() {
		t.Fatalf("key deletion affected logs or account ownership: %v, %v", storedLogs, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE api_keys SET exp = $1 WHERE id = $2`,
		time.Now().Add(-time.Minute).Unix(), readKey.ID); err != nil {
		t.Fatal(err)
	}
	if expired := get(readKey.ID); !expired.ExpiresAt.Before(time.Now()) {
		t.Fatal("expired key metadata was not retained")
	}
	if _, err := client.VerifyAPIKey(ctx, readKey.APIKey); !errors.Is(err, authoriser.ErrInvalidAPIKey) {
		t.Fatalf("expired key is still valid: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		status, _ := manage(method, "/logkey/"+uuid.NewString(), "")
		if status != http.StatusNotFound {
			t.Fatalf("%s missing key returned HTTP %d", method, status)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatalf("soft deletion removed API key rows: %d", rows)
	}
}
