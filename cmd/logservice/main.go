package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"
	"logging-service/internal/logservice/httpapi"
	"logging-service/internal/postgresstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("log ingress stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url := os.Getenv("POSTGRES_URL")

	if url == "" || os.Getenv("LOGSERVICE_API_PORT") == "" {
		return fmt.Errorf("POSTGRES_URL (or POSTGRES_MIGRATION_URL) and LOGSERVICE_API_PORT are required")
	}
	authorizer, err := configuredAuthorizer(
		os.Getenv("LOGSERVICE_ENV"),
		os.Getenv("LOGSERVICE_AUTH_MODE"),
		os.Getenv("AUTHORISER_URL"),
	)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(root, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(startup, url)
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	db := postgresstore.New(pool)
	service := logservice.New(db)
	router := httpapi.NewRouter(service, authorizer, slog.Default())
	server := &http.Server{
		Addr: ":" + os.Getenv("LOGSERVICE_API_PORT"), Handler: router,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			closeErr := server.Close()
			return fmt.Errorf("server shutdown: %w", errors.Join(err, closeErr))
		}
		return nil
	}
}

const mockAccountID = "00000000-0000-4000-8000-000000000001"

type mockAuthorizer struct{}

func (mockAuthorizer) Authorize(_ context.Context, apiKey string, _ authoriser.Scope) (authoriser.Authorization, error) {
	if apiKey == "" {
		return authoriser.Authorization{}, nil
	}
	return authoriser.Authorization{Authorized: true, AccountID: mockAccountID}, nil
}

func configuredAuthorizer(environment, mode, authoriserURL string) (authoriser.Authorizer, error) {
	if mode == "" || mode == "authoriser" {
		return authoriser.NewClient(authoriserURL)
	}
	if mode != "mock" {
		return nil, fmt.Errorf("LOGSERVICE_AUTH_MODE must be authoriser or mock")
	}
	if environment != "local" && environment != "test" {
		return nil, fmt.Errorf("mock authorisation is allowed only when LOGSERVICE_ENV is local or test")
	}
	return mockAuthorizer{}, nil
}
