package tokenhandler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testKeys(t *testing.T) (*Signer, *Verifier) {
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
	signer, err := NewSigner(privatePath, "test-issuer", "management")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(publicPath, "test-issuer", "management")
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier
}

func TestTokenValidation(t *testing.T) {
	signer, verifier := testKeys(t)
	id := uuid.New()
	token, err := signer.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	if token.TokenType != "Bearer" || token.ExpiresIn != 900 {
		t.Fatalf("unexpected token metadata: type=%s expiry=%d", token.TokenType, token.ExpiresIn)
	}
	got, err := verifier.Verify(token.AccessToken)
	if err != nil || got != id {
		t.Fatalf("verify subject = %s, error = %v", got, err)
	}
	for _, name := range []string{"expired", "missing expiry", "wrong issuer", "wrong audience", "nil subject", "missing issued at", "future issued at", "future not before", "wrong signature", "wrong algorithm", "malformed"} {
		t.Run(name, func(t *testing.T) {
			claims := jwt.RegisteredClaims{
				Issuer: "test-issuer", Audience: jwt.ClaimStrings{"management"}, Subject: id.String(),
				IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			}
			method := jwt.SigningMethod(jwt.SigningMethodEdDSA)
			var key interface{} = signer.key
			switch name {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))
			case "missing expiry":
				claims.ExpiresAt = nil
			case "wrong issuer":
				claims.Issuer = "other"
			case "wrong audience":
				claims.Audience = jwt.ClaimStrings{"logs"}
			case "nil subject":
				claims.Subject = uuid.Nil.String()
			case "missing issued at":
				claims.IssuedAt = nil
			case "future issued at":
				claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
			case "future not before":
				claims.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
			case "wrong signature":
				other, _ := testKeys(t)
				key = other.key
			case "wrong algorithm":
				method, key = jwt.SigningMethodHS256, []byte("not-an-asymmetric-key")
			}
			raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if name == "malformed" {
				raw = "log_not-a-jwt"
			}
			if _, err := verifier.Verify(raw); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("invalid token accepted: %v", err)
			}
		})
	}
}

type accountStub struct{ err error }

func (a *accountStub) GetActiveAccount(_ context.Context, id uuid.UUID) (Account, error) {
	return Account{ID: id, Username: "owner", Role: "member"}, a.err
}

func TestIssuanceClientAndAccountGate(t *testing.T) {
	signer, verifier := testKeys(t)
	accounts := &accountStub{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewRouter(signer, "internal-service-credential", accounts, logger))
	defer server.Close()
	client, err := NewClient(server.URL, "internal-service-credential")
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	token, err := client.Issue(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := verifier.Verify(token.AccessToken); err != nil || got != id {
		t.Fatalf("issued token verification failed: %v", err)
	}
	router := gin.New()
	router.Use(RequestLimits())
	router.GET("/private", RequireAccount(verifier, accounts, logger), func(c *gin.Context) {
		c.JSON(200, gin.H{"account_id": CurrentAccount(c).ID.String()})
	})
	for _, tc := range []struct {
		name       string
		header     string
		accountErr error
		status     int
	}{
		{"valid", "Bearer " + token.AccessToken, nil, 200},
		{"missing", "", nil, 401},
		{"wrong type", "Basic " + token.AccessToken, nil, 401},
		{"logging key", "Bearer log_example", nil, 401},
		{"suspended", "Bearer " + token.AccessToken, ErrAccountIneligible, 403},
		{"database unavailable", "Bearer " + token.AccessToken, errors.New("private database error"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts.err = tc.accountErr
			request := httptest.NewRequest(http.MethodGet, "/private", nil)
			request.Header.Set("Authorization", tc.header)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if tc.status == 200 && !strings.Contains(response.Body.String(), id.String()) {
				t.Fatal("authenticated account identity was lost")
			}
		})
	}
	for _, tc := range []struct {
		key        string
		accountErr error
	}{
		{"wrong", nil},
		{"internal-service-credential", ErrAccountIneligible},
		{"internal-service-credential", errors.New("database down")},
	} {
		accounts.err = tc.accountErr
		client, err := NewClient(server.URL, tc.key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Issue(context.Background(), id); err == nil {
			t.Fatal("issuance did not fail closed")
		}
	}
	response, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("health requires authentication")
	}
}

func TestKeyConfigurationFailsClosed(t *testing.T) {
	if _, err := NewSigner("", "", ""); err == nil {
		t.Fatal("missing signing configuration accepted")
	}
	if _, err := NewVerifier("", "", ""); err == nil {
		t.Fatal("missing verification configuration accepted")
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("invalid key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner(path, "issuer", "audience"); err == nil {
		t.Fatal("invalid signing key accepted")
	}
	if _, err := NewVerifier(path, "issuer", "audience"); err == nil {
		t.Fatal("invalid verification key accepted")
	}
}
