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
	"logging-service/internal/authoriser/httpapi"
	"logging-service/internal/postgresstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("authoriser stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url, port := os.Getenv("POSTGRES_URL"), os.Getenv("AUTHORISER_API_PORT")
	if url == "" || port == "" {
		return fmt.Errorf("POSTGRES_URL and AUTHORISER_API_PORT are required")
	}

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

	service := authoriser.NewService(postgresstore.New(pool))
	router := httpapi.NewRouter(service, slog.Default())
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
		return nil
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", errors.Join(err, server.Close()))
		}
		return nil
	}
}
