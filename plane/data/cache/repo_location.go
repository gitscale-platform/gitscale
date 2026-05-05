package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// RepoLocation holds the storage-routing fields for a repository.
// Version guards against stale schema in the cache; a mismatch is treated as a miss.
type RepoLocation struct {
	Version        int    `json:"v"`
	ReplicaSetID   string `json:"replica_set_id"`
	HomeRegion     string `json:"home_region"`
	ACLFingerprint string `json:"acl_fingerprint"`
}

const repoLocationVersion = 1

// repoLocationMissBytes is the negative-cache sentinel value.
var repoLocationMissBytes = []byte(`{"v":1,"_miss":true}`)

var errVersionMismatch = errors.New("cache: payload version mismatch")

// repoLocationGroup collapses concurrent cache misses for the same key within
// a single process. Worst case: one loader call per pod per TTL expiry.
var repoLocationGroup singleflight.Group

// GetRepoLocation fetches the repo location from cache, falling back to loader
// on a miss. Negative results (loader returns nil) are cached for
// RepoLocationNotFoundTTL to prevent hammering the source of truth.
// Returns (nil, ErrNotFound) for cached or fresh misses.
func GetRepoLocation(
	ctx context.Context,
	c CacheStore,
	repoID uuid.UUID,
	loader func(ctx context.Context, id uuid.UUID) (*RepoLocation, error),
) (*RepoLocation, error) {
	key := fmt.Sprintf(RepoLocationKey, repoID)

	b, err := c.Get(ctx, key)
	if err == nil {
		loc, miss, decErr := decodeRepoLocation(b)
		if decErr == nil {
			if miss {
				return nil, ErrNotFound
			}
			return loc, nil
		}
		// version mismatch or corrupt payload → treat as miss, fall through to loader
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("cache: get repo location: %w", err)
	}

	v, loadErr, _ := repoLocationGroup.Do(key, func() (any, error) {
		return loader(ctx, repoID)
	})
	if loadErr != nil {
		return nil, loadErr
	}

	loc, _ := v.(*RepoLocation)
	if loc == nil {
		_ = c.Set(ctx, key, repoLocationMissBytes, RepoLocationNotFoundTTL)
		return nil, ErrNotFound
	}

	loc.Version = repoLocationVersion
	payload, err := json.Marshal(loc)
	if err != nil {
		return nil, fmt.Errorf("cache: marshal repo location: %w", err)
	}
	_ = c.Set(ctx, key, payload, RepoLocationTTL)
	return loc, nil
}

// SetRepoLocation writes a repo location directly into the cache.
// Callers use this after a write to the source of truth to prime the cache.
func SetRepoLocation(ctx context.Context, c CacheStore, repoID uuid.UUID, loc RepoLocation) error {
	loc.Version = repoLocationVersion
	payload, err := json.Marshal(loc)
	if err != nil {
		return fmt.Errorf("cache: marshal repo location: %w", err)
	}
	return c.Set(ctx, fmt.Sprintf(RepoLocationKey, repoID), payload, RepoLocationTTL)
}

type repoLocationRaw struct {
	Version int  `json:"v"`
	Miss    bool `json:"_miss"`
}

func decodeRepoLocation(b []byte) (loc *RepoLocation, miss bool, err error) {
	var raw repoLocationRaw
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false, fmt.Errorf("cache: unmarshal repo location header: %w", err)
	}
	if raw.Version != repoLocationVersion {
		return nil, false, errVersionMismatch
	}
	if raw.Miss {
		return nil, true, nil
	}
	var out RepoLocation
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false, fmt.Errorf("cache: unmarshal repo location body: %w", err)
	}
	return &out, false, nil
}
