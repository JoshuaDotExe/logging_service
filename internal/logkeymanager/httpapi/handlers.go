package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"logging-service/internal/logkeymanager"
	"logging-service/internal/tokenhandler"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *logkeymanager.Service
	logger  *slog.Logger
}

func NewHandler(service *logkeymanager.Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

type createLogKeyRequest struct {
	AccountID string          `json:"account_id"`
	Scopes    json.RawMessage `json:"scopes"`
}

func (h *Handler) CreateLogKey(c *gin.Context) {
	var request createLogKeyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid log key request"})
		return
	}
	accountID := tokenhandler.CurrentAccount(c).ID
	if request.AccountID != "" {
		requestedID, err := uuid.Parse(request.AccountID)
		if err != nil || requestedID == uuid.Nil {
			h.respondError(c, logkeymanager.ErrInvalidAccountID)
			return
		}
		if requestedID != accountID {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account ownership required"})
			return
		}
	}
	var scopes []string
	if request.Scopes != nil {
		if err := json.Unmarshal(request.Scopes, &scopes); err != nil || scopes == nil {
			h.respondError(c, logkeymanager.ErrInvalidScopes)
			return
		}
	}
	key, err := h.service.CreateLogKey(c.Request.Context(), accountID, scopes)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, key)
}

func (h *Handler) GetLogKeysByAccountID(c *gin.Context) {
	accountID, err := uuid.Parse(c.Param("accountID"))
	if err != nil {
		h.respondError(c, logkeymanager.ErrInvalidAccountID)
		return
	}
	if accountID != tokenhandler.CurrentAccount(c).ID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account ownership required"})
		return
	}
	keys, err := h.service.GetLogKeysByAccountID(c.Request.Context(), accountID)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

func (h *Handler) GetLogKeyByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.respondError(c, logkeymanager.ErrInvalidKeyID)
		return
	}
	key, err := h.service.GetLogKeyByID(c.Request.Context(), id)
	if err != nil {
		h.respondError(c, err)
		return
	}
	if !h.ownsKey(c, key) {
		return
	}
	c.JSON(http.StatusOK, key)
}

func (h *Handler) DeleteLogKey(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.respondError(c, logkeymanager.ErrInvalidKeyID)
		return
	}
	key, err := h.service.GetLogKeyByID(c.Request.Context(), id)
	if err != nil {
		h.respondError(c, err)
		return
	}
	if !h.ownsKey(c, key) {
		return
	}
	if err := h.service.DeleteLogKey(c.Request.Context(), id); err != nil {
		h.respondError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *Handler) ownsKey(c *gin.Context, key logkeymanager.LogKey) bool {
	if key.AccountID != tokenhandler.CurrentAccount(c).ID.String() {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "log key not found"})
		return false
	}
	return true
}

func (h *Handler) respondError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, logkeymanager.ErrInvalidAccountID):
		status, message = http.StatusBadRequest, "invalid account_id"
	case errors.Is(err, logkeymanager.ErrInvalidKeyID):
		status, message = http.StatusBadRequest, "invalid key id"
	case errors.Is(err, logkeymanager.ErrInvalidScopes):
		status, message = http.StatusBadRequest, "scopes must contain read, write, or delete without duplicates"
	case errors.Is(err, logkeymanager.ErrKeyNotFound):
		status, message = http.StatusNotFound, "log key not found"
	default:
		h.logger.Error("log key request failed", "error", err)
	}
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}
