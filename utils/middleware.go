package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type responseCapture struct {
	gin.ResponseWriter
	body []byte
}

func (r *responseCapture) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return r.ResponseWriter.Write(data)
}

func CacheMiddleware(rdb *redis.Client, ttl time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		cacheKey := "cache:" + c.Request.RequestURI
		ctx := context.Background()

		val, err := rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			c.Data(http.StatusOK, "application/json", []byte(val))
			c.Abort()
			return
		}

		// Capture the response
		capture := &responseCapture{ResponseWriter: c.Writer}
		c.Writer = capture

		c.Next()

		// Cache the response
		if c.Writer.Status() == http.StatusOK {
			rdb.Set(ctx, cacheKey, capture.body, ttl)
		}
	}
}

func RateLimitIteratorMiddleWare(rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawKey := c.GetHeader("X-API-Key")
		if rawKey == "" {
			c.JSON(400, gin.H{"error": "missing X-API-Key header"})
			c.Abort()
			return
		}

		keyHash := hashAPIKey(rawKey)
		ctx := context.Background()
		data, err := rdb.HGetAll(ctx, "ingress:key:"+keyHash).Result()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			c.Abort()
			return
		}
		if len(data) == 0 {
			c.JSON(404, gin.H{"error": "API key not found"})
			c.Abort()
			return
		}

		rateLimitWindow, _ := strconv.ParseInt(data["rate_limit_window"], 10, 64)
		rateLimitMax, _ := strconv.ParseInt(data["rate_limit_max"], 10, 64)
		rateLimitCount, _ := strconv.ParseInt(data["rate_limit_count"], 10, 64)
		rateLimitExpiresAt, _ := strconv.ParseInt(data["rate_limit_expires_at"], 10, 64)

		now := time.Now().Unix()
		if rateLimitExpiresAt <= now {
			rateLimitCount = 0
			rateLimitExpiresAt = now + rateLimitWindow
			rdb.HSet(ctx, "ingress:key:"+keyHash, map[string]interface{}{
				"rate_limit_count":      rateLimitCount,
				"rate_limit_expires_at": rateLimitExpiresAt,
			})
		}

		if rateLimitCount >= rateLimitMax {
			c.JSON(429, gin.H{"error": "rate limit exceeded"})
			c.Abort()
			return
		}

		count, err := rdb.HIncrBy(
			ctx,
			"ingress:key:"+keyHash,
			"rate_limit_count",
			1,
		).Result()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			c.Abort()
			return
		}

		c.Set("rate_limit_count", count)
		c.Next()
	}
}

func hashAPIKey(rawKey string) string {
	digest := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(digest[:])
}
