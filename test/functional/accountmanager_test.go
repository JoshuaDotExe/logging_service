package functional

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateAccount(t *testing.T) {
	baseURL := os.Getenv("ACCOUNTMANAGER_URL")
	if baseURL == "" {
		t.Skip("set ACCOUNTMANAGER_URL to run against the deployed service")
	}

	id := uuid.NewString()
	payload, err := json.Marshal(map[string]string{
		"username": "test-" + id,
		"email":    "test-" + id + "@example.test",
		"password": "a-long-enough-password",
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(
		strings.TrimRight(baseURL, "/")+"/account",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		t.Fatalf("POST /account: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /account status = %d, want %d; body: %s",
			resp.StatusCode, http.StatusCreated, body)
	}
}
