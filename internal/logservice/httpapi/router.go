package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"

	"github.com/gin-gonic/gin"
)

func NewRouter(
	service *logservice.Service,
	authorizer authoriser.Authorizer,
	logger *slog.Logger,
) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	handlers := &Handlers{
		service:    service,
		authorizer: authorizer,
		logger:     logger,
	}

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	router.POST(
		"/logs",
		handlers.requireAuthorization(authoriser.ScopeWrite),
		handlers.ingest,
	)

	router.GET(
		"/logs",
		handlers.requireAuthorization(authoriser.ScopeNone),
		handlers.getLogs,
	)

	router.DELETE(
		"/log",
		handlers.requireAuthorization(authoriser.ScopeNone),
		handlers.deleteLog,
	)

	router.DELETE(
		"/logs",
		handlers.requireAuthorization(authoriser.ScopeNone),
		handlers.deleteLogsByAccountID,
	)

	router.DELETE(
		"/logs/account",
		handlers.requireAuthorization(authoriser.ScopeNone),
		handlers.deleteLogsByAccountID,
	)

	return router
}
