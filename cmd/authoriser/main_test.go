package main

import "testing"

func TestRunRequiresConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
		port string
	}{
		{name: "missing database", port: "8083"},
		{name: "missing port", url: "postgres://localhost/test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POSTGRES_URL", tt.url)
			t.Setenv("AUTHORISER_API_PORT", tt.port)
			if err := run(); err == nil {
				t.Fatal("authoriser started without required configuration")
			}
		})
	}
}
