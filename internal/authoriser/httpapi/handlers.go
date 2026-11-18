package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"logging-service/internal/authoriser"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *authoriser.Service
	logger  *slog.Logger
}

func NewHandler(service *authoriser.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

type verifyAPIKeyRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

func (h *Handler) VerifyAPIKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var request verifyAPIKeyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid API key request"})
		return
	}

	key, err := h.service.VerifyAPIKey(c.Request.Context(), request.APIKey)
	if errors.Is(err, authoriser.ErrInvalidAPIKey) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
		return
	}
	if err != nil {
		h.logger.Error("API key verification failed", "error", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, key)
}
