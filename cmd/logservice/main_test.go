package main

import (
	"testing"

	"logging-service/internal/authoriser"
)

func TestConfiguredAuthorizerRequiresLocalMockMode(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		mode        string
		wantErr     bool
	}{
		{name: "local mock", environment: "local", mode: "mock"},
		{name: "test mock", environment: "test", mode: "mock"},
		{name: "mock disabled", environment: "local", wantErr: true},
		{name: "mock unavailable in production", environment: "production", mode: "mock", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configuredAuthorizer(tt.environment, tt.mode, "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("configuredAuthorizer() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}
			result, err := got.Authorize(t.Context(), "test-key", authoriser.ScopeWrite)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Authorized || result.AccountID != mockAccountID {
				t.Fatalf("authorization = %+v; want authorized for %q", result, mockAccountID)
			}
			result, err = got.Authorize(t.Context(), "", authoriser.ScopeWrite)
			if err != nil {
				t.Fatal(err)
			}
			if result.Authorized {
				t.Fatal("mock authorizer accepted an empty API key")
			}
		})
	}
}

func TestConfiguredAuthorizerUsesRealServiceByDefault(t *testing.T) {
	for _, mode := range []string{"", "authoriser"} {
		got, err := configuredAuthorizer("production", mode, "http://authoriser:8083")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*authoriser.Client); !ok {
			t.Fatalf("authorizer = %T, want real authoriser client", got)
		}
	}
}
