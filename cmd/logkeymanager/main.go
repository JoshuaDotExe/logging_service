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

	"github.com/jackc/pgx/v5/pgxpool"

	"logging-service/internal/logkeymanager"
	"logging-service/internal/logkeymanager/httpapi"
	"logging-service/internal/postgresstore"
	"logging-service/internal/tokenhandler"
)

func main() {
	if err := run(); err != nil {
		slog.Error("log key manager stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url := os.Getenv("POSTGRES_URL")
	port := os.Getenv("LOGKEYMANAGER_API_PORT")
	if url == "" || port == "" {
		return fmt.Errorf("POSTGRES_URL and LOGKEYMANAGER_API_PORT are required")
	}
	verifier, err := tokenhandler.NewVerifier(os.Getenv("JWT_PUBLIC_KEY_PATH"), os.Getenv("JWT_ISSUER"), os.Getenv("JWT_AUDIENCE"))
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
	service := logkeymanager.NewService(db)
	router := httpapi.NewRouter(service, slog.Default(), verifier, db)

	server := &http.Server{
		Addr: ":" + port, Handler: router,
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
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", errors.Join(err, server.Close()))
		}
	}

	return nil
}
