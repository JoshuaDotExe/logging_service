// internal/redisstore/store.go
package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"logging-service/internal/logservice"
	"logging-service/internal/postgresstore"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

type Store struct {
	client *redis.Client
	keys   APIKeyReader
}

type APIKeyReader interface {
	GetAPIKeyByAPIKeyHash(context.Context, string) (*postgresstore.APIKeyEntry, error)
}

func New(client *redis.Client, keys APIKeyReader) *Store {
	return &Store{client: client, keys: keys}
}

func configKey(hash string) string   { return "ingress:config:{" + hash + "}" }
func disabledKey(hash string) string { return "ingress:disabled:{" + hash + "}" }
func counterKey(hash string) string  { return "ingress:rate-limit:{" + hash + "}" }

var initializeConfig = redis.NewScript(`
if redis.call("EXISTS", KEYS[2]) == 1 then return -1 end
if redis.call("EXISTS", KEYS[1]) == 1 then return 0 end
local now = tonumber(redis.call("TIME")[1])
local ttl = math.min(60, tonumber(ARGV[3]) - now)
if ttl <= 0 then return -1 end
redis.call("HSET", KEYS[1], "account_id", ARGV[1], "created_at", ARGV[2],
    "expires_at", ARGV[3], "rate_limit_window", ARGV[4], "rate_limit_max", ARGV[5])
redis.call("EXPIRE", KEYS[1], ttl)
return 1
`)

func (s *Store) StoreAPIKey(ctx context.Context, data APIKeyDBEntry) error {
	if err := validateConfig(data); err != nil {
		return err
	}
	result, err := initializeConfig.Run(ctx, s.client,
		[]string{configKey(data.KeyHash), disabledKey(data.KeyHash)},
		data.AccountID, data.CreatedAt, data.ExpiresAt, data.RateLimitWindow, data.RateLimitMax,
	).Int()
	if err != nil {
		return fmt.Errorf("initialize API key cache: %w", err)
	}
	if result == -1 {
		return logservice.ErrUnauthorized
	}
	return nil
}

var readConfig = redis.NewScript(`
if redis.call("EXISTS", KEYS[2]) == 1 then return {"soft_deleted", "1"} end
return redis.call("HGETALL", KEYS[1])
`)

func (s *Store) GetByAPIKey(ctx context.Context, hashedKey string) (APIKeyDBEntry, error) {
	fields, err := readConfig.Run(ctx, s.client,
		[]string{configKey(hashedKey), disabledKey(hashedKey)}).StringSlice()
	if err != nil {
		return APIKeyDBEntry{}, fmt.Errorf("read API key cache: %w", err)
	}
	if len(fields) == 0 {
		return APIKeyDBEntry{}, redis.Nil
	}
	if len(fields)%2 != 0 {
		return APIKeyDBEntry{}, fmt.Errorf("invalid API key cache field pairs")
	}
	result := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		result[fields[i]] = fields[i+1]
	}
	if result["soft_deleted"] == "1" {
		return APIKeyDBEntry{KeyHash: hashedKey, SoftDeleted: true}, nil
	}
	entry := APIKeyDBEntry{
		KeyHash: hashedKey,
	}
	if v, ok := result["account_id"]; ok {
		entry.AccountID = v
	}
	for field, target := range map[string]*int64{
		"created_at":        &entry.CreatedAt,
		"expires_at":        &entry.ExpiresAt,
		"rate_limit_window": &entry.RateLimitWindow,
		"rate_limit_max":    &entry.RateLimitMax,
	} {
		if v, ok := result[field]; ok {
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return APIKeyDBEntry{}, fmt.Errorf("parse Redis field %q: %w", field, err)
			}
			*target = parsed
		} else {
			return APIKeyDBEntry{}, fmt.Errorf("missing Redis field %q", field)
		}
	}
	if err := validateConfig(entry); err != nil {
		return APIKeyDBEntry{}, err
	}
	return entry, nil
}

func (s *Store) DeleteByAPIKey(ctx context.Context, hashedKey string) error {
	return s.client.Del(ctx, configKey(hashedKey)).Err()
}

var disableKey = redis.NewScript(`
redis.call("SET", KEYS[2], "1")
redis.call("DEL", KEYS[1])
return 1
`)

func (s *Store) SoftDeleteByAPIKeyHash(ctx context.Context, hashedKey string) error {
	if err := disableKey.Run(ctx, s.client, []string{configKey(hashedKey), disabledKey(hashedKey)}).Err(); err != nil {
		return fmt.Errorf("disable cached API key: %w", err)
	}
	return nil
}

func validateConfig(data APIKeyDBEntry) error {
	if data.KeyHash == "" || data.AccountID == "" || data.ExpiresAt <= 0 ||
		data.RateLimitWindow <= 0 || data.RateLimitWindow > 86400 ||
		data.RateLimitMax <= 0 || data.RateLimitMax > 1000000000 {
		return fmt.Errorf("invalid API key configuration")
	}
	return nil
}

func (s *Store) ResolveActive(ctx context.Context, rawKey string) (logservice.Identity, error) {
	if rawKey == "" {
		return logservice.Identity{}, logservice.ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(rawKey))
	hash := hex.EncodeToString(digest[:])
	entry, err := s.GetByAPIKey(ctx, hash)
	if errors.Is(err, redis.Nil) {
		key, lookupErr := s.keys.GetAPIKeyByAPIKeyHash(ctx, hash)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return logservice.Identity{}, logservice.ErrUnauthorized
		}
		if lookupErr != nil {
			return logservice.Identity{}, fmt.Errorf("load API key: %w", lookupErr)
		}
		if key.SoftDeleted || key.DeactivatedAt != nil || key.Exp <= time.Now().Unix() {
			return logservice.Identity{}, logservice.ErrUnauthorized
		}
		if err := s.StoreAPIKey(ctx, APIKeyDBEntry{
			KeyHash: hash, AccountID: key.AccountID, CreatedAt: key.CreatedAt.Unix(),
			ExpiresAt: key.Exp, RateLimitWindow: key.RateLimitWindow, RateLimitMax: key.RateLimitMax,
		}); err != nil {
			return logservice.Identity{}, err
		}
		entry, err = s.GetByAPIKey(ctx, hash)
	}
	if errors.Is(err, redis.Nil) {
		return logservice.Identity{}, logservice.ErrUnauthorized
	}
	if err != nil {
		return logservice.Identity{}, err
	}
	if entry.SoftDeleted || entry.ExpiresAt <= time.Now().Unix() {
		return logservice.Identity{}, logservice.ErrUnauthorized
	}
	return logservice.Identity{AccountID: entry.AccountID, KeyHash: hash}, nil
}

var allowRequest = redis.NewScript(`
if redis.call("EXISTS", KEYS[2]) == 1 then return -1 end
local config = redis.call("HMGET", KEYS[1], "account_id", "expires_at", "rate_limit_window", "rate_limit_max")
if not config[1] then return -1 end
local expires = tonumber(config[2])
local window = tonumber(config[3])
local maximum = tonumber(config[4])
if not expires or not window or not maximum or window <= 0 or window > 86400 or maximum <= 0 or maximum > 1000000000 then
    return redis.error_reply("invalid rate limit configuration")
end
local now = tonumber(redis.call("TIME")[1])
if config[1] ~= ARGV[1] or expires <= now then return -1 end
local count = tonumber(redis.call("GET", KEYS[3]) or "0")
if not count or count < 0 then return redis.error_reply("invalid rate limit counter") end
if count >= maximum then return 0 end
redis.call("INCR", KEYS[3])
if count == 0 then redis.call("EXPIRE", KEYS[3], window) end
return 1
`)

func (s *Store) Allow(ctx context.Context, identity logservice.Identity) (bool, error) {
	result, err := allowRequest.Run(ctx, s.client,
		[]string{configKey(identity.KeyHash), disabledKey(identity.KeyHash), counterKey(identity.KeyHash)},
		identity.AccountID,
	).Int()
	if err != nil {
		return false, fmt.Errorf("consume rate limit: %w", err)
	}
	if result == -1 {
		return false, logservice.ErrUnauthorized
	}
	return result == 1, nil
}
