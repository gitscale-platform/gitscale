package cache

import (
	"context"
	"errors"
	_ "embed"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed lua/cas.lua
var casLuaScript string

var casScript = redis.NewScript(casLuaScript)

// RedisConfig holds connection parameters for the Redis store.
// UseCluster must be set explicitly; URL parsing alone is fragile.
type RedisConfig struct {
	URL         string
	UseCluster  bool
	PoolSize    int
	DialTimeout time.Duration
}

// redisStore wraps a go-redis universal client behind CacheStore.
type redisStore struct {
	client redis.UniversalClient
}

// NewRedisStore constructs a CacheStore backed by Redis.
// For cluster topologies set cfg.UseCluster = true.
// The returned store does NOT apply any namespace prefix; wrap with
// WithNamespace before use.
func NewRedisStore(cfg RedisConfig) (CacheStore, error) {
	poolSize := cfg.PoolSize
	if poolSize == 0 {
		poolSize = 10
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}

	var client redis.UniversalClient
	if cfg.UseCluster {
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:       []string{cfg.URL},
			PoolSize:    poolSize,
			DialTimeout: dialTimeout,
		})
	} else {
		opt, err := redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("cache: parse redis URL: %w", err)
		}
		opt.PoolSize = poolSize
		opt.DialTimeout = dialTimeout
		client = redis.NewClient(opt)
	}

	return &redisStore{client: client}, nil
}

func (r *redisStore) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cache: get %q: %w", key, err)
	}
	return val, nil
}

func (r *redisStore) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("cache: mget: %w", err)
	}
	out := make([][]byte, len(vals))
	for i, v := range vals {
		if v == nil {
			out[i] = nil
			continue
		}
		s, ok := v.(string)
		if !ok {
			out[i] = nil
			continue
		}
		out[i] = []byte(s)
	}
	return out, nil
}

func (r *redisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache: set %q: %w", key, err)
	}
	return nil
}

func (r *redisStore) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache: delete %q: %w", key, err)
	}
	return nil
}

func (r *redisStore) CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
	ttlMs := ttl.Milliseconds()
	result, err := casScript.Run(ctx, r.client, []string{key}, expected, replacement, ttlMs).Int()
	if err != nil {
		return false, fmt.Errorf("cache: cas %q: %w", key, err)
	}
	return result == 1, nil
}

func (r *redisStore) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache: ping: %w", err)
	}
	return nil
}
