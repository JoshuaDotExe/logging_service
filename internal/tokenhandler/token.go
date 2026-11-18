package tokenhandler

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var ErrInvalidToken = errors.New("invalid access token")

const Lifetime = 15 * time.Minute

type Token struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type Signer struct {
	key      ed25519.PrivateKey
	issuer   string
	audience string
}

func NewSigner(path, issuer, audience string) (*Signer, error) {
	if path == "" || issuer == "" || audience == "" {
		return nil, errors.New("JWT_PRIVATE_KEY_PATH, JWT_ISSUER and JWT_AUDIENCE are required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	key, err := jwt.ParseEdPrivateKeyFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("parse Ed25519 signing key: %w", err)
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("signing key must be Ed25519")
	}
	return &Signer{key: privateKey, issuer: issuer, audience: audience}, nil
}

func (s *Signer) Issue(id uuid.UUID) (Token, error) {
	if id == uuid.Nil {
		return Token{}, errors.New("account ID is required")
	}
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer: s.issuer, Subject: id.String(), Audience: jwt.ClaimStrings{s.audience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(Lifetime)), ID: uuid.NewString(),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(s.key)
	if err != nil {
		return Token{}, fmt.Errorf("sign access token: %w", err)
	}
	return Token{AccessToken: raw, TokenType: "Bearer", ExpiresIn: int64(Lifetime.Seconds())}, nil
}

type Verifier struct {
	key      ed25519.PublicKey
	issuer   string
	audience string
}

func NewVerifier(path, issuer, audience string) (*Verifier, error) {
	if path == "" || issuer == "" || audience == "" {
		return nil, errors.New("JWT_PUBLIC_KEY_PATH, JWT_ISSUER and JWT_AUDIENCE are required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read verification key: %w", err)
	}
	key, err := jwt.ParseEdPublicKeyFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("parse Ed25519 verification key: %w", err)
	}
	publicKey, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("verification key must be Ed25519")
	}
	return &Verifier{key: publicKey, issuer: issuer, audience: audience}, nil
}

func (v *Verifier) Verify(raw string) (uuid.UUID, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(_ *jwt.Token) (interface{}, error) {
		return v.key, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid || claims.IssuedAt == nil {
		return uuid.Nil, ErrInvalidToken
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ErrInvalidToken
	}
	return id, nil
}
