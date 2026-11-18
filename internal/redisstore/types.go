package redisstore

import "time"

type DBEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type DBKey struct {
	Key string `json:"key"`
}

type CreateAPIKey struct {
	AccountID       string `json:"account_id"`
	RateLimitWindow int64  `json:"rate_limit_window"`
	RateLimitMax    int64  `json:"rate_limit_max"`
}

type APIKeyDBEntry struct {
	KeyHash            string    `json:"key"`
	AccountID          string    `json:"account_id"`
	CreatedAt          int64     `json:"created_at"`
	ExpiresAt          int64     `json:"expires_at"`
	RateLimitWindow    int64     `json:"rate_limit_window"`
	RateLimitMax       int64     `json:"rate_limit_max"`
	RateLimitCount     int64     `json:"rate_limit_count"`
	RateLimitExpiresAt int64     `json:"rate_limit_expires_at"`
	DeactivatedAt      time.Time `json:"deactivated_at"`
	DeactivatedBy      string    `json:"deactivated_by"`
	SoftDeleted        bool      `json:"soft_deleted"`
}
