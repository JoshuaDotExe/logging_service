package authoriser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrInvalidAPIKey = errors.New("invalid API key")

type Scope string

const (
	ScopeNone  Scope = ""
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

type Authorization struct {
	Authorized bool
	AccountID  string
}

type Authorizer interface {
	Authorize(ctx context.Context, apiKey string, scope Scope) (Authorization, error)
}

type Credentials struct {
	AccountID string
	Scopes    []string
}

type APIKey struct {
	Key       string   `json:"api_key"`
	AccountID string   `json:"account_id"`
	Scopes    []string `json:"scopes"`
}

type APIKeyReader interface {
	GetAPIKeyCredentials(ctx context.Context, keyHash string) (Credentials, error)
}

type Service struct {
	reader APIKeyReader
}

func NewService(reader APIKeyReader) *Service {
	return &Service{reader: reader}
}

func (s *Service) VerifyAPIKey(ctx context.Context, apiKey string) (APIKey, error) {
	if apiKey == "" {
		return APIKey{}, ErrInvalidAPIKey
	}

	digest := sha256.Sum256([]byte(apiKey))
	credentials, err := s.reader.GetAPIKeyCredentials(ctx, hex.EncodeToString(digest[:]))
	if err != nil {
		return APIKey{}, fmt.Errorf("verify API key: %w", err)
	}

	return APIKey{
		Key:       apiKey,
		AccountID: credentials.AccountID,
		Scopes:    append([]string{}, credentials.Scopes...),
	}, nil
}
