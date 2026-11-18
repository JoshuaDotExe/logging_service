package main

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMonitorResults(t *testing.T) {
	for _, tt := range []struct {
		name    string
		results []result
		success bool
		want    string
	}{
		{"success", []result{{status: http.StatusCreated}}, true, "HTTP statuses: map[201:1] | Errors: 0"},
		{"HTTP failure", []result{{status: http.StatusUnauthorized}}, false, "HTTP statuses: map[401:1] | Errors: 0"},
		{"connection failure", []result{{err: errors.New("connection failed\nretry")}}, false, `Errors: 1 | Last error: "connection failed\nretry"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			results := make(chan result, len(tt.results))
			for _, r := range tt.results {
				results <- r
			}
			close(results)
			var output bytes.Buffer
			if got := monitorResults(&output, results, len(tt.results)); got != tt.success {
				t.Fatalf("success = %v, want %v", got, tt.success)
			}
			text := output.String()
			if !strings.HasPrefix(text, "\r\x1b[2KRPS: ") ||
				!strings.Contains(text, "Completed: 1/1") ||
				!strings.Contains(text, tt.want) ||
				strings.Count(text, "\n") != 1 {
				t.Fatalf("unexpected output: %q", text)
			}
		})
	}
}

func TestMonitorResultsRefreshesInPlace(t *testing.T) {
	results := make(chan result, 1)
	results <- result{status: http.StatusCreated}
	go func() {
		time.Sleep(1100 * time.Millisecond)
		close(results)
	}()
	var output bytes.Buffer
	if !monitorResults(&output, results, 1) {
		t.Fatal("expected success")
	}
	text := output.String()
	if strings.Count(text, "\r\x1b[2K") < 2 || strings.Count(text, "\n") != 1 {
		t.Fatalf("expected in-place refresh followed by one newline, got %q", text)
	}
}
