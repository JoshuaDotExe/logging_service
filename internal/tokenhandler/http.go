package tokenhandler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var ErrAccountIneligible = errors.New("account is not eligible")

type Account struct {
	ID       uuid.UUID
	Username string
	Role     string
}

type AccountReader interface {
	GetActiveAccount(context.Context, uuid.UUID) (Account, error)
}

type TokenVerifier interface {
	Verify(string) (uuid.UUID, error)
}

const accountContextKey = "authenticated-account"

func CurrentAccount(c *gin.Context) Account {
	return c.MustGet(accountContextKey).(Account)
}

func RequireAccount(verifier TokenVerifier, accounts AccountReader, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bearer token required"})
			return
		}
		id, err := verifier.Verify(parts[1])
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid access token"})
			return
		}
		account, err := accounts.GetActiveAccount(c.Request.Context(), id)
		if errors.Is(err, ErrAccountIneligible) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account is not active"})
			return
		}
		if err != nil {
			logger.Error("check authenticated account", "error", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account verification unavailable"})
			return
		}
		c.Set(accountContextKey, account)
		c.Next()
	}
}

func RequestLimits() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func NewRouter(signer *Signer, serviceKey string, accounts AccountReader, logger *slog.Logger) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), RequestLimits())
	expected := sha256.Sum256([]byte(serviceKey))
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.POST("/token", func(c *gin.Context) {
		actual := sha256.Sum256([]byte(c.GetHeader("X-Token-Service-Key")))
		if serviceKey == "" || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		var request struct {
			AccountID uuid.UUID `json:"account_id"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || request.AccountID == uuid.Nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid account_id"})
			return
		}
		if _, err := accounts.GetActiveAccount(c.Request.Context(), request.AccountID); err != nil {
			if errors.Is(err, ErrAccountIneligible) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account is not active"})
			} else {
				logger.Error("check token account", "error", err)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account verification unavailable"})
			}
			return
		}
		token, err := signer.Issue(request.AccountID)
		if err != nil {
			logger.Error("issue access token", "error", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "token issuance failed"})
			return
		}
		c.JSON(http.StatusOK, token)
	})
	return r
}

type Client struct {
	url    string
	key    string
	client *http.Client
}

func NewClient(baseURL, serviceKey string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || serviceKey == "" {
		return nil, errors.New("TOKENHANDLER_URL and TOKEN_SERVICE_KEY must be configured")
	}
	return &Client{
		url: strings.TrimRight(baseURL, "/") + "/token", key: serviceKey,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (c *Client) Issue(ctx context.Context, id uuid.UUID) (Token, error) {
	body, err := json.Marshal(struct {
		AccountID uuid.UUID `json:"account_id"`
	}{id})
	if err != nil {
		return Token{}, fmt.Errorf("encode token request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Token-Service-Key", c.key)
	response, err := c.client.Do(request)
	if err != nil {
		return Token{}, fmt.Errorf("request access token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("token service returned status %d", response.StatusCode)
	}
	var token Token
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&token); err != nil {
		return Token{}, fmt.Errorf("decode access token: %w", err)
	}
	if token.AccessToken == "" || token.TokenType != "Bearer" || token.ExpiresIn <= 0 {
		return Token{}, errors.New("token service returned an invalid response")
	}
	return token, nil
}
