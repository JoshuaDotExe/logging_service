package authoriser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"
)

type Client struct {
	endpoint string
	http     *http.Client
}

var _ Authorizer = (*Client)(nil)

func NewClient(baseURL string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("AUTHORISER_URL must be a valid absolute HTTP(S) URL")
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("AUTHORISER_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	endpoint, err := url.JoinPath(base.String(), "authorise")
	if err != nil {
		return nil, fmt.Errorf("configure authoriser endpoint: %w", err)
	}
	return &Client{
		endpoint: endpoint,
		http: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) VerifyAPIKey(ctx context.Context, apiKey string) (APIKey, error) {
	if apiKey == "" {
		return APIKey{}, ErrInvalidAPIKey
	}
	body, err := json.Marshal(struct {
		Key string `json:"api_key"`
	}{Key: apiKey})
	if err != nil {
		return APIKey{}, fmt.Errorf("encode authoriser request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return APIKey{}, fmt.Errorf("create authoriser request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return APIKey{}, fmt.Errorf("call authoriser: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return APIKey{}, ErrInvalidAPIKey
	}
	if response.StatusCode != http.StatusOK {
		return APIKey{}, fmt.Errorf("authoriser returned HTTP %d", response.StatusCode)
	}

	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return APIKey{}, fmt.Errorf("read authoriser response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return APIKey{}, fmt.Errorf("authoriser response exceeds size limit")
	}
	var key APIKey
	if err := json.Unmarshal(data, &key); err != nil {
		return APIKey{}, fmt.Errorf("decode authoriser response: %w", err)
	}
	if key.Key != apiKey || key.AccountID == "" || key.Scopes == nil {
		return APIKey{}, fmt.Errorf("invalid authoriser response")
	}
	return key, nil
}

func (c *Client) Authorize(ctx context.Context, apiKey string, scope Scope) (Authorization, error) {
	key, err := c.VerifyAPIKey(ctx, apiKey)
	if errors.Is(err, ErrInvalidAPIKey) {
		return Authorization{}, nil
	}
	if err != nil {
		return Authorization{}, err
	}
	return Authorization{
		Authorized: scope == ScopeNone || slices.Contains(key.Scopes, string(scope)),
		AccountID:  key.AccountID,
	}, nil
}
