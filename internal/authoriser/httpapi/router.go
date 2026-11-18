package httpapi

import (
	"log/slog"
	"net/http"

	"logging-service/internal/authoriser"

	"github.com/gin-gonic/gin"
)

func NewRouter(service *authoriser.Service, logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery(), requestLimits())
	handler := NewHandler(service, logger)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.POST("/authorise", handler.VerifyAPIKey)

	return router
}
