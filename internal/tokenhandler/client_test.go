package tokenhandler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestClientRejectsBadResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable", 503, `{}`},
		{"redirect", 307, `{}`},
		{"malformed", 200, `{`},
		{"missing token", 200, `{"token_type":"Bearer","expires_in":900}`},
		{"wrong type", 200, `{"access_token":"token","token_type":"Basic","expires_in":900}`},
		{"invalid lifetime", 200, `{"access_token":"token","token_type":"Bearer","expires_in":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 307 {
					w.Header().Set("Location", "/redirect-target")
				}
				if r.URL.Path != "/token" {
					t.Error("client followed redirect")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "internal-test-credential")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Issue(context.Background(), uuid.New()); err == nil {
				t.Fatal("invalid token-service response was accepted")
			}
		})
	}
	for _, address := range []string{"", "file:///tmp/token", "http://user:password@example.test", "http://example.test?query=value", "http://example.test#fragment"} {
		if _, err := NewClient(address, "credential"); err == nil {
			t.Errorf("invalid token service URL was accepted: %s", address)
		}
	}
}
