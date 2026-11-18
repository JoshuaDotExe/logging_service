package postgresstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"logging-service/internal/accountmanager"
	accountapi "logging-service/internal/accountmanager/httpapi"
	"logging-service/internal/logkeymanager"
	keyapi "logging-service/internal/logkeymanager/httpapi"
	"logging-service/internal/tokenhandler"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func newAccountTokenKeys(t *testing.T) (*tokenhandler.Signer, *tokenhandler.Verifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	for path, block := range map[string]*pem.Block{
		privatePath: {Type: "PRIVATE KEY", Bytes: privateDER},
		publicPath:  {Type: "PUBLIC KEY", Bytes: publicDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	signer, err := tokenhandler.NewSigner(privatePath, "test-issuer", "management")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := tokenhandler.NewVerifier(publicPath, "test-issuer", "management")
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier
}

func TestAccountAuthenticationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := newAPIKeyTestPool(t, ctx)
	applyTestMigration(t, ctx, pool, "000001_create_logs.up.sql")
	store := New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, verifier := newAccountTokenKeys(t)
	tokenServer := httptest.NewServer(tokenhandler.NewRouter(signer, "internal-test-credential", store, logger))
	defer tokenServer.Close()
	issuer, err := tokenhandler.NewClient(tokenServer.URL, "internal-test-credential")
	if err != nil {
		t.Fatal(err)
	}
	login, err := accountmanager.NewLoginService(store, issuer)
	if err != nil {
		t.Fatal(err)
	}
	router := accountapi.NewRouter(accountmanager.NewService(store), login, store,
		tokenhandler.RequireAccount(verifier, store, logger), logger)
	keys := keyapi.NewRouter(logkeymanager.NewService(store), logger, verifier, store)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	id, other := uuid.New(), uuid.New()
	for _, a := range []struct {
		id   uuid.UUID
		name string
	}{{id, "owner"}, {other, "other"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, username, email, password_hash, status)
			VALUES ($1, $2, $3, $4, 'active')`, a.id, a.name, a.name+"@example.test", string(hash)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		response := request("POST", "/login", `{"username":"owner","password":"incorrect"}`, "")
		if response.Code != 401 {
			t.Fatalf("wrong password status = %d", response.Code)
		}
	}
	if response := request("POST", "/login", `{"username":"owner","password":"correct-password"}`, ""); response.Code != 401 {
		t.Fatalf("locked account logged in: %d", response.Code)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET locked_until = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	response := request("POST", "/login", `{"username":"OWNER","password":"correct-password"}`, "")
	if response.Code != 200 {
		t.Fatalf("login status = %d: %s", response.Code, response.Body.String())
	}
	var token tokenhandler.Token
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	if got, err := verifier.Verify(token.AccessToken); err != nil || got != id {
		t.Fatalf("wrong login subject: %v", err)
	}
	createKey := func() int {
		req := httptest.NewRequest("POST", "/logkey", bytes.NewBufferString(`{}`)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		response := httptest.NewRecorder()
		keys.ServeHTTP(response, req)
		return response.Code
	}
	if status := createKey(); status != 201 {
		t.Fatalf("login JWT could not create an API key: %d", status)
	}
	response = request("GET", "/account", `{"username":"other"}`, token.AccessToken)
	var details accountmanager.AccountDetails
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || details.ID != id {
		t.Fatal("account lookup trusted caller-supplied identity")
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET status = 'suspended' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", "/account", "", token.AccessToken); response.Code != 403 {
		t.Fatal("existing JWT bypassed account suspension")
	}
	if status := createKey(); status != 403 {
		t.Fatalf("suspended account created a key: %d", status)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET status = 'active' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if response := request("DELETE", "/account", `{"username":"other"}`, token.AccessToken); response.Code != 204 {
		t.Fatalf("own deletion status = %d", response.Code)
	}
	if _, err := store.GetActiveAccount(ctx, id); !errors.Is(err, tokenhandler.ErrAccountIneligible) {
		t.Fatal("own account was not deleted")
	}
	if _, err := store.GetActiveAccount(ctx, other); err != nil {
		t.Fatal("other account was affected by deletion")
	}
	if response := request("GET", "/account", "", token.AccessToken); response.Code != 403 {
		t.Fatal("deleted account retained management access")
	}
	if status := createKey(); status != 403 {
		t.Fatalf("deleted account created a key: %d", status)
	}
}
