package httpapi

import (
	"log/slog"
	"net/http"

	"logging-service/internal/logkeymanager"
	"logging-service/internal/tokenhandler"

	"github.com/gin-gonic/gin"
)

func NewRouter(service *logkeymanager.Service, logger *slog.Logger, verifier tokenhandler.TokenVerifier, accounts tokenhandler.AccountReader) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), tokenhandler.RequestLimits())
	handler := NewHandler(service, logger)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	keys := r.Group("/logkey", tokenhandler.RequireAccount(verifier, accounts, logger))
	keys.POST("", handler.CreateLogKey)
	keys.GET("/account/:accountID", handler.GetLogKeysByAccountID)
	keys.GET("/:id", handler.GetLogKeyByID)
	keys.DELETE("/:id", handler.DeleteLogKey)
	return r
}
