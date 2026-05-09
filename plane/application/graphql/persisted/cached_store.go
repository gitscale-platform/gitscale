package persisted

import (
	"context"
	"errors"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/google/uuid"
)

// DefaultTTL is the cache lifetime applied on Get/Put. 24h matches the
// spec; the source-of-truth row is immutable so a longer TTL would be
// safe but raises memory pressure on the hot bucket.
const DefaultTTL = 24 * time.Hour

// cacheKeyPrefix namespaces persisted-query cache keys away from other
// CacheStore consumers (identity cache, repo-location cache).
const cacheKeyPrefix = "graphql:persisted:"

// CachedStore is a read-through wrapper. Get hits the cache first; on miss
// it falls back to the underlying Store and back-fills the cache. Put
// writes through to the underlying store and primes the cache.
//
// Cache failures are not fatal: a Get error from the cache is logged via
// the cache implementation's own surface (CacheStore returns ErrNotFound
// only on a miss; transport errors propagate, and we treat any non-
// ErrNotFound error from cache.Get as a miss for availability).
type CachedStore struct {
	Inner Store
	Cache cache.CacheStore
	TTL   time.Duration
}

// NewCachedStore composes inner with the given cache.
func NewCachedStore(inner Store, c cache.CacheStore) *CachedStore {
	return &CachedStore{Inner: inner, Cache: c, TTL: DefaultTTL}
}

// Get implements Store.
func (c *CachedStore) Get(ctx context.Context, hash string) (string, error) {
	if c.Cache != nil {
		// Cache transport failures fall through to the source of truth;
		// we never propagate cache errors to the caller because the source
		// row is authoritative.
		v, err := c.Cache.Get(ctx, cacheKeyPrefix+hash)
		if err == nil {
			return string(v), nil
		}
		_ = errors.Is(err, cache.ErrNotFound) // documented but unused branch
	}
	q, err := c.Inner.Get(ctx, hash)
	if err != nil {
		return "", err
	}
	if c.Cache != nil {
		_ = c.Cache.Set(ctx, cacheKeyPrefix+hash, []byte(q), c.TTL)
	}
	return q, nil
}

// Put implements Store. Writes through to the underlying store first; on
// success, primes the cache with the new value.
func (c *CachedStore) Put(ctx context.Context, hash, query string, registeredBy uuid.UUID) error {
	if err := c.Inner.Put(ctx, hash, query, registeredBy); err != nil {
		return err
	}
	if c.Cache != nil {
		_ = c.Cache.Set(ctx, cacheKeyPrefix+hash, []byte(query), c.TTL)
	}
	return nil
}
