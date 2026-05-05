package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// IdentityCacheEntry holds the resolved identity for a principal.
// Version guards against stale schema in the cache.
type IdentityCacheEntry struct {
	Version     int    `json:"v"`
	PrincipalID string `json:"principal_id"`
	OrgID       string `json:"org_id"`
	Roles       []string `json:"roles"`
}

const identityVersion = 1

var identityMissBytes = []byte(`{"v":1,"_miss":true}`)

var identityGroup singleflight.Group

// GetIdentity fetches a principal's identity from cache, falling back to loader
// on a miss. Negative results are cached for IdentityNotFoundTTL.
// Returns (nil, ErrNotFound) for cached or fresh misses.
func GetIdentity(
	ctx context.Context,
	c CacheStore,
	principalID uuid.UUID,
	loader func(ctx context.Context, id uuid.UUID) (*IdentityCacheEntry, error),
) (*IdentityCacheEntry, error) {
	key := fmt.Sprintf(IdentityKey, principalID)

	b, err := c.Get(ctx, key)
	if err == nil {
		entry, miss, decErr := decodeIdentity(b)
		if decErr == nil {
			if miss {
				return nil, ErrNotFound
			}
			return entry, nil
		}
		// version mismatch or corrupt → treat as miss
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("cache: get identity: %w", err)
	}

	v, loadErr, _ := identityGroup.Do(key, func() (any, error) {
		return loader(ctx, principalID)
	})
	if loadErr != nil {
		return nil, loadErr
	}

	entry, _ := v.(*IdentityCacheEntry)
	if entry == nil {
		_ = c.Set(ctx, key, identityMissBytes, IdentityNotFoundTTL)
		return nil, ErrNotFound
	}

	entry.Version = identityVersion
	payload, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("cache: marshal identity: %w", err)
	}
	_ = c.Set(ctx, key, payload, IdentityTTL)
	return entry, nil
}

// SetIdentity writes an identity entry directly into the cache.
func SetIdentity(ctx context.Context, c CacheStore, principalID uuid.UUID, entry IdentityCacheEntry) error {
	entry.Version = identityVersion
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("cache: marshal identity: %w", err)
	}
	return c.Set(ctx, fmt.Sprintf(IdentityKey, principalID), payload, IdentityTTL)
}

// InvalidateIdentity removes a principal's identity from the cache.
// Called by the identity-cache-invalidator consumer on mutation events.
func InvalidateIdentity(ctx context.Context, c CacheStore, principalID uuid.UUID) error {
	return c.Delete(ctx, fmt.Sprintf(IdentityKey, principalID))
}

type identityRaw struct {
	Version int  `json:"v"`
	Miss    bool `json:"_miss"`
}

func decodeIdentity(b []byte) (entry *IdentityCacheEntry, miss bool, err error) {
	var raw identityRaw
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false, fmt.Errorf("cache: unmarshal identity header: %w", err)
	}
	if raw.Version != identityVersion {
		return nil, false, errVersionMismatch
	}
	if raw.Miss {
		return nil, true, nil
	}
	var out IdentityCacheEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false, fmt.Errorf("cache: unmarshal identity body: %w", err)
	}
	return &out, false, nil
}
