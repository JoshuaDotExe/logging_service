package main

import "testing"

func TestRunRequiresConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name      string
		url       string
		port      string
		publicKey string
	}{
		{name: "missing database", port: "8082"},
		{name: "missing port", url: "postgres://localhost/test"},
		{name: "missing JWT public key", url: "postgres://localhost/test", port: "8082"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POSTGRES_URL", tt.url)
			t.Setenv("LOGKEYMANAGER_API_PORT", tt.port)
			t.Setenv("JWT_PUBLIC_KEY_PATH", tt.publicKey)
			if err := run(); err == nil {
				t.Fatal("logkeymanager started without required configuration")
			}
		})
	}
}
