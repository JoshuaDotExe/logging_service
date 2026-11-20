package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handlers struct {
	service    *logservice.Service
	authorizer authoriser.Authorizer
	logger     *slog.Logger
}

type ingestRequest struct {
	Message string `json:"message" binding:"required"`
}

type egressRequest struct {
	CursorID        string `form:"cursor_id"`
	CursorCreatedAt string `form:"cursor_created_at"`
	Limit           int    `form:"limit"`
}

type deleteLogRequest struct {
	LogID string `form:"log_id" binding:"required"`
}

func (h *Handlers) ingest(c *gin.Context) {
	value, exists := c.Get(identityContextKey)
	identity, valid := value.(logservice.Identity)
	if !exists || !valid {
		h.logger.Error("missing ingestion identity")
		c.AbortWithStatusJSON(
			http.StatusInternalServerError,
			gin.H{"error": "internal server error"},
		)
		return
	}

	var body ingestRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.AbortWithStatusJSON(
			http.StatusBadRequest,
			gin.H{"error": "invalid log request"},
		)
		return
	}

	if err := h.service.Ingest(
		c.Request.Context(),
		identity,
		body.Message,
	); err != nil {
		h.respondError(c, err)
		return
	}

	c.Status(http.StatusCreated)
}

func (h *Handlers) respondError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"

	switch {
	case errors.Is(err, logservice.ErrUnauthorized):
		status = http.StatusUnauthorized
		message = "invalid API key"
	case errors.Is(err, logservice.ErrInvalidMessage):
		status = http.StatusBadRequest
		message = "invalid log message"
	case errors.Is(err, logservice.ErrInvalidCursor):
		status = http.StatusBadRequest
		message = "invalid cursor"
	default:
		h.logger.Error("ingestion request failed", "error", err)
	}

	c.AbortWithStatusJSON(status, gin.H{"error": message})
}

func (h *Handlers) getLogs(c *gin.Context) {
	value, exists := c.Get(identityContextKey)
	identity, valid := value.(logservice.Identity)
	if !exists || !valid {
		h.logger.Error("missing logs identity")
		c.AbortWithStatusJSON(
			http.StatusInternalServerError,
			gin.H{"error": "internal server error"},
		)
		return
	}
	var req egressRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.AbortWithStatusJSON(
			http.StatusBadRequest,
			gin.H{"error": "invalid query parameters"},
		)
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10 // Default limit if not provided or invalid
	}

	cursor := logservice.LogCursor{}
	hasCursorID := req.CursorID != ""
	hasCursorTime := req.CursorCreatedAt != ""
	if hasCursorID != hasCursorTime {
		c.AbortWithStatusJSON(
			http.StatusBadRequest,
			gin.H{"error": "cursor_id and cursor_created_at must be provided together"},
		)
		return
	}
	if hasCursorID {
		cursorID, err := uuid.Parse(req.CursorID)
		if err != nil {
			c.AbortWithStatusJSON(
				http.StatusBadRequest,
				gin.H{"error": "invalid cursor_id"},
			)
			return
		}
		cursorCreatedAt, err := time.Parse(time.RFC3339Nano, req.CursorCreatedAt)
		if err != nil {
			c.AbortWithStatusJSON(
				http.StatusBadRequest,
				gin.H{"error": "invalid cursor_created_at"},
			)
			return
		}
		cursor = logservice.LogCursor{
			ID:        cursorID,
			CreatedAt: cursorCreatedAt,
		}
	}

	logs, nextPage, err := h.service.PaginatedEgress(
		c.Request.Context(),
		identity,
		limit,
		cursor,
	)
	if err != nil {
		h.respondError(c, err)
		return
	}
	if nextPage {
		last := logs[len(logs)-1]
		c.JSON(http.StatusOK, gin.H{
			"logs":              logs,
			"cursor_id":         last.ID,
			"cursor_created_at": last.CreatedAt,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

func (h *Handlers) deleteLog(c *gin.Context) {
	var req deleteLogRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.AbortWithStatusJSON(
			http.StatusBadRequest,
			gin.H{"error": "invalid query parameters"},
		)
		return
	}
	value, exists := c.Get(identityContextKey)
	identity, valid := value.(logservice.Identity)
	if !exists || !valid {
		h.logger.Error("missing deletion identity")
		c.AbortWithStatusJSON(
			http.StatusInternalServerError,
			gin.H{"error": "internal server error"},
		)
		return
	}
	accountID := uuid.MustParse(identity.AccountID)

	logID, err := uuid.Parse(req.LogID)
	if err != nil {
		c.AbortWithStatusJSON(
			http.StatusBadRequest,
			gin.H{"error": "invalid log_id"},
		)
		return
	}

	err = h.service.DeleteLog(c.Request.Context(), logID, accountID)
	if err != nil {
		h.respondError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *Handlers) deleteLogsByAccountID(c *gin.Context) {
	value, exists := c.Get(identityContextKey)
	identity, valid := value.(logservice.Identity)
	if !exists || !valid {
		h.logger.Error("missing deletion identity")
		c.AbortWithStatusJSON(
			http.StatusInternalServerError,
			gin.H{"error": "internal server error"},
		)
		return
	}

	err := h.service.DeleteLogsByAccountID(c.Request.Context(), uuid.MustParse(identity.AccountID))
	if err != nil {
		h.respondError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}
