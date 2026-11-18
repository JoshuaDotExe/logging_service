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

	"logging-service/internal/postgresstore"
	"logging-service/internal/tokenhandler"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("token handler stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	signer, err := tokenhandler.NewSigner(os.Getenv("JWT_PRIVATE_KEY_PATH"), os.Getenv("JWT_ISSUER"), os.Getenv("JWT_AUDIENCE"))
	if err != nil {
		return err
	}
	url, port, key := os.Getenv("POSTGRES_URL"), os.Getenv("TOKENHANDLER_API_PORT"), os.Getenv("TOKEN_SERVICE_KEY")
	if url == "" || port == "" || len(key) < 32 {
		return errors.New("POSTGRES_URL, TOKENHANDLER_API_PORT and TOKEN_SERVICE_KEY (at least 32 characters) are required")
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(root, 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(startup, config)
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	server := &http.Server{
		Addr: ":" + port, Handler: tokenhandler.NewRouter(signer, key, postgresstore.New(pool), slog.Default()),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("token handler server: %w", err)
		}
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown token handler: %w", errors.Join(err, server.Close()))
		}
	}
	return nil
}
