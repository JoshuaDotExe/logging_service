package httpapi

import (
	"errors"
	"log/slog"
	"logging-service/internal/accountmanager"
	"logging-service/internal/tokenhandler"

	"github.com/gin-gonic/gin"
)

func NewHandler(service *accountmanager.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

type Handler struct {
	service *accountmanager.Service
	logger  *slog.Logger
}

type accountCreateRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) CreateAccount(c *gin.Context) {
	var req accountCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := h.service.CreateAccount(req.Username, req.Email, req.Password); err != nil {
		if errors.Is(err, accountmanager.ErrInvalidPassword) {
			c.JSON(400, gin.H{"error": "password must contain 12 to 72 bytes"})
			return
		}
		h.logger.Error("create account failed", "error", err)
		c.JSON(500, gin.H{"error": "account creation failed"})
		return
	}
	c.Status(201)
}

func (h *Handler) DeleteAccount(c *gin.Context) {
	accountID := tokenhandler.CurrentAccount(c).ID
	if err := h.service.DeleteAccount(accountID); err != nil {
		h.logger.Error("delete account failed", "error", err)
		c.JSON(500, gin.H{"error": "account deletion failed"})
		return
	}
	c.Status(204)
}
