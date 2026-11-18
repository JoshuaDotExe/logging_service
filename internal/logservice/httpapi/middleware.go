package httpapi

import (
	"net/http"

	"logging-service/internal/authoriser"
	"logging-service/internal/logservice"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const identityContextKey = "logservice.identity"

func (h *Handlers) requireAuthorization(scope authoriser.Scope) gin.HandlerFunc {
	return func(c *gin.Context) {
		decision, err := h.authorizer.Authorize(
			c.Request.Context(),
			c.GetHeader("X-API-Key"),
			scope,
		)
		if err != nil {
			h.logger.Error("authorisation check failed", "error", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authorization unavailable"})
			return
		}
		if !decision.Authorized {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
			return
		}
		accountID, err := uuid.Parse(decision.AccountID)
		if err != nil || accountID == uuid.Nil {
			h.logger.Error("authoriser returned invalid account ID", "account_id", decision.AccountID)
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "invalid authorization response"})
			return
		}

		c.Set(identityContextKey, logservice.Identity{AccountID: accountID.String()})
		c.Next()
	}
}
