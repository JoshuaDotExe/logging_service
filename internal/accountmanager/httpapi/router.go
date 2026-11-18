package httpapi

import (
	"log/slog"
	"logging-service/internal/accountmanager"
	"logging-service/internal/tokenhandler"

	"github.com/gin-gonic/gin"
)

func NewRouter(service *accountmanager.Service, login *accountmanager.LoginService, store accountmanager.LoginStore, auth gin.HandlerFunc, logger *slog.Logger) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), tokenhandler.RequestLimits())
	handler := NewHandler(service, logger)

	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.POST("/account", handler.CreateAccount)
	r.POST("/login", loginHandler(login, logger))
	r.GET("/account", auth, func(c *gin.Context) {
		details, err := store.GetAccountDetailsByID(c.Request.Context(), tokenhandler.CurrentAccount(c).ID)
		if err != nil {
			logger.Error("get own account", "error", err)
			c.JSON(500, gin.H{"error": "account lookup failed"})
			return
		}
		c.JSON(200, details)
	})
	r.DELETE("/account", auth, func(c *gin.Context) {
		if err := store.SoftDeleteAccountByID(c.Request.Context(), tokenhandler.CurrentAccount(c).ID); err != nil {
			logger.Error("delete own account", "error", err)
			c.JSON(500, gin.H{"error": "account deletion failed"})
			return
		}
		c.Status(204)
	})

	return r
}
