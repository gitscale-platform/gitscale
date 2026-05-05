package cache

import (
	"context"
	"time"
)

// namespacedStore prepends "gitscale:<env>:" to every key before delegating
// to the inner CacheStore. This is the single place the env prefix is applied;
// key templates in keys.go must NOT include it.
type namespacedStore struct {
	inner  CacheStore
	prefix string
}

// WithNamespace wraps inner with an env-specific key prefix.
// Construct once at startup:
//
//	raw := cache.NewRedisStore(cfg)
//	store := cache.WithNamespace(raw, cfg.Env) // "gitscale:prod:repo:loc:..."
func WithNamespace(inner CacheStore, env string) CacheStore {
	return &namespacedStore{inner: inner, prefix: "gitscale:" + env + ":"}
}

func (n *namespacedStore) prefixed(key string) string {
	return n.prefix + key
}

func (n *namespacedStore) Get(ctx context.Context, key string) ([]byte, error) {
	return n.inner.Get(ctx, n.prefixed(key))
}

func (n *namespacedStore) MGet(ctx context.Context, keys []string) ([][]byte, error) {
	prefixed := make([]string, len(keys))
	for i, k := range keys {
		prefixed[i] = n.prefixed(k)
	}
	return n.inner.MGet(ctx, prefixed)
}

func (n *namespacedStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return n.inner.Set(ctx, n.prefixed(key), value, ttl)
}

func (n *namespacedStore) Delete(ctx context.Context, key string) error {
	return n.inner.Delete(ctx, n.prefixed(key))
}

func (n *namespacedStore) CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
	return n.inner.CompareAndSwap(ctx, n.prefixed(key), expected, replacement, ttl)
}

func (n *namespacedStore) Ping(ctx context.Context) error {
	return n.inner.Ping(ctx)
}
