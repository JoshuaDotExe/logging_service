package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"logging-service/internal/accountmanager"
	"logging-service/internal/accountmanager/httpapi"
	"logging-service/internal/postgresstore"
	"logging-service/internal/tokenhandler"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("Account Manager stopped:", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("POSTGRES_URL") == "" || os.Getenv("ACCOUNTMANAGER_API_PORT") == "" {
		return fmt.Errorf("POSTGRES_URL and ACCOUNTMANAGER_API_PORT are required")
	}
	verifier, err := tokenhandler.NewVerifier(os.Getenv("JWT_PUBLIC_KEY_PATH"), os.Getenv("JWT_ISSUER"), os.Getenv("JWT_AUDIENCE"))
	if err != nil {
		return err
	}
	issuer, err := tokenhandler.NewClient(os.Getenv("TOKENHANDLER_URL"), os.Getenv("TOKEN_SERVICE_KEY"))
	if err != nil {
		return err
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url := os.Getenv("POSTGRES_URL")

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
	service := accountmanager.NewService(db)
	login, err := accountmanager.NewLoginService(db, issuer)
	if err != nil {
		return err
	}
	router := httpapi.NewRouter(service, login, db, tokenhandler.RequireAccount(verifier, db, slog.Default()), slog.Default())

	server := &http.Server{
		Addr: ":" + os.Getenv("ACCOUNTMANAGER_API_PORT"), Handler: router,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}
	}

	return nil
}
