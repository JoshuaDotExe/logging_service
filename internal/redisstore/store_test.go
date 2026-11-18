package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"logging-service/internal/logservice"
	"logging-service/internal/postgresstore"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

type keyReader struct {
	entry        postgresstore.APIKeyEntry
	err          error
	calls        atomic.Int64
	beforeReturn func()
}

func (r *keyReader) GetAPIKeyByAPIKeyHash(context.Context, string) (*postgresstore.APIKeyEntry, error) {
	r.calls.Add(1)
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	return &r.entry, r.err
}

func TestRedisAuthentication(t *testing.T) {
	addr := os.Getenv("REDISSTORE_TEST_ADDR")
	if addr == "" {
		t.Skip("set REDISSTORE_TEST_ADDR to an isolated Redis instance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("test-key-%d", time.Now().UnixNano())
	digest := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(digest[:])
	cleanup := func() {
		if err := client.Del(ctx, configKey(hash), disabledKey(hash), counterKey(hash)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	defer cleanup()
	reader := &keyReader{entry: postgresstore.APIKeyEntry{
		AccountID: "account", Key: hash, CreatedAt: time.Now(), Exp: time.Now().Add(time.Hour).Unix(),
		RateLimitWindow: 60, RateLimitMax: 10,
	}}
	store := New(client, reader)
	identity, err := store.ResolveActive(ctx, raw)
	if err != nil || identity.AccountID != "account" {
		t.Fatalf("resolve miss: %+v %v", identity, err)
	}
	if _, err := store.ResolveActive(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if reader.calls.Load() != 1 {
		t.Fatal("cache hit queried PostgreSQL")
	}
	if err := store.DeleteByAPIKey(ctx, hash); err != nil {
		t.Fatal(err)
	}
	var refillWG sync.WaitGroup
	for i := 0; i < 25; i++ {
		refillWG.Add(1)
		go func() {
			defer refillWG.Done()
			got, err := store.ResolveActive(ctx, raw)
			if err != nil || got != identity {
				t.Errorf("concurrent refill: %+v %v", got, err)
			}
		}()
	}
	refillWG.Wait()
	ttl, err := client.TTL(ctx, configKey(hash)).Result()
	if err != nil || ttl <= 0 || ttl > 60*time.Second {
		t.Fatalf("invalid TTL: %v %v", ttl, err)
	}

	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, err := store.Allow(ctx, identity)
			if err != nil {
				t.Errorf("allow: %v", err)
			}
			if allowed {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("accepted %d requests, want 10", accepted.Load())
	}
	if err := store.DeleteByAPIKey(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveActive(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.Allow(ctx, identity); err != nil || allowed {
		t.Fatalf("refill reset quota: %v %v", allowed, err)
	}
	if err := client.Del(ctx, counterKey(hash)).Err(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.Allow(ctx, identity); err != nil || !allowed {
		t.Fatalf("new window: %v %v", allowed, err)
	}
	counterTTL, err := client.TTL(ctx, counterKey(hash)).Result()
	if err != nil || counterTTL <= 0 || counterTTL > 60*time.Second {
		t.Fatalf("invalid counter TTL: %v %v", counterTTL, err)
	}
	if err := client.PExpire(ctx, counterKey(hash), time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if allowed, err := store.Allow(ctx, identity); err != nil || !allowed {
		t.Fatalf("expired window did not restart: %v %v", allowed, err)
	}

	if err := store.SoftDeleteByAPIKeyHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveActive(ctx, raw); !errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("disabled cache: %v", err)
	}
	if allowed, err := store.Allow(ctx, identity); allowed || !errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("disabled limiter: %v %v", allowed, err)
	}
	cleanup()
	reader.beforeReturn = func() {
		if err := store.SoftDeleteByAPIKeyHash(ctx, hash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ResolveActive(ctx, raw); !errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("stale refill resurrected key: %v", err)
	}
	reader.beforeReturn = nil
	cleanup()
	reader.err = pgx.ErrNoRows
	if _, err := store.ResolveActive(ctx, raw); !errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("missing key: %v", err)
	}
	reader.err = errors.New("database unavailable")
	if _, err := store.ResolveActive(ctx, raw); !errors.Is(err, reader.err) || errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("database failure: %v", err)
	}
	reader.err = nil
	reader.entry.Exp = time.Now().Add(-time.Second).Unix()
	if _, err := store.ResolveActive(ctx, raw); !errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("expired key: %v", err)
	}
	reader.entry.Exp = time.Now().Add(20 * time.Second).Unix()
	if _, err := store.ResolveActive(ctx, raw); err != nil {
		t.Fatal(err)
	}
	ttl, err = client.TTL(ctx, configKey(hash)).Result()
	if err != nil || ttl <= 0 || ttl > 20*time.Second {
		t.Fatalf("expiry capped TTL: %v %v", ttl, err)
	}
	if err := client.HDel(ctx, configKey(hash), "rate_limit_max").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveActive(ctx, raw); err == nil {
		t.Fatal("malformed cache accepted")
	}
}

func TestRedisFailureDoesNotFallback(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer client.Close()
	reader := &keyReader{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := New(client, reader).ResolveActive(ctx, "key")
	if err == nil || errors.Is(err, logservice.ErrUnauthorized) {
		t.Fatalf("expected infrastructure error, got %v", err)
	}
	if reader.calls.Load() != 0 {
		t.Fatal("Redis error triggered database fallback")
	}
}
