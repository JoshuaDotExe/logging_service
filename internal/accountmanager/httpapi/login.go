package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"logging-service/internal/accountmanager"

	"github.com/gin-gonic/gin"
)

func loginHandler(service *accountmanager.LoginService, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			Username string `json:"username" binding:"required,max=64"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login request"})
			return
		}
		token, err := service.Login(c.Request.Context(), request.Username, request.Password)
		if errors.Is(err, accountmanager.ErrInvalidCredentials) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials or account unavailable"})
			return
		}
		if err != nil {
			logger.Error("login failed", "error", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "login unavailable"})
			return
		}
		c.JSON(http.StatusOK, token)
	}
}
