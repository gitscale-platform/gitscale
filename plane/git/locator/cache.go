package locator

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/google/uuid"
)

// CacheLocator resolves repo locations via cache.GetRepoLocation, falling
// back to next on a miss. The fallback's result is written through to the
// cache by GetRepoLocation, so subsequent lookups for the same repo hit L1.
//
// A negative result from next (ErrRepoNotFound) is negative-cached for
// cache.RepoLocationNotFoundTTL to absorb hammering on missing repos.
type CacheLocator struct {
	store cache.CacheStore
	next  RepoLocator
}

// NewCacheLocator returns a CacheLocator using store as L1 and next as L2.
// Both arguments are required; passing nil panics on first Resolve.
func NewCacheLocator(store cache.CacheStore, next RepoLocator) *CacheLocator {
	return &CacheLocator{store: store, next: next}
}

// Resolve returns the file-server address for repoID. repoID must be a UUID
// string; an invalid UUID is reported with a wrapping error (not
// ErrRepoNotFound). A confirmed not-found is reported as ErrRepoNotFound.
func (l *CacheLocator) Resolve(ctx context.Context, repoID string) (FileServerAddr, error) {
	id, err := uuid.Parse(repoID)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: invalid repo_id %q: %w", repoID, err)
	}

	loc, err := cache.GetRepoLocation(ctx, l.store, id, func(ctx context.Context, id uuid.UUID) (*cache.RepoLocation, error) {
		addr, nextErr := l.next.Resolve(ctx, id.String())
		if errors.Is(nextErr, ErrRepoNotFound) {
			return nil, nil
		}
		if nextErr != nil {
			return nil, nextErr
		}
		return &cache.RepoLocation{
			ReplicaSetID: addr.ReplicaSetID,
			HomeRegion:   addr.HomeRegion,
		}, nil
	})

	if errors.Is(err, cache.ErrNotFound) {
		return FileServerAddr{}, ErrRepoNotFound
	}
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: cache resolve: %w", err)
	}

	return FileServerAddr{
		ReplicaSetID: loc.ReplicaSetID,
		HomeRegion:   loc.HomeRegion,
		Addr:         loc.ReplicaSetID + ":8075",
	}, nil
}
