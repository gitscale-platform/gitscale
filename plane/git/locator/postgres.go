package locator

import (
	"context"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// MetadataLocator resolves repo locations from the MetadataStore. Use as the
// fallback inside a CacheLocator; do not call it directly on the hot path.
type MetadataLocator struct {
	store store.MetadataStore
}

// NewMetadataLocator returns a locator backed by store.
func NewMetadataLocator(s store.MetadataStore) *MetadataLocator {
	return &MetadataLocator{store: s}
}

// Resolve loads the Repository row for repoID and returns its routing address.
// A row not present in the table is reported as ErrRepoNotFound.
func (l *MetadataLocator) Resolve(ctx context.Context, repoID string) (FileServerAddr, error) {
	id, err := uuid.Parse(repoID)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: invalid repo_id %q: %w", repoID, err)
	}
	repo, err := l.store.Repositories().GetByID(ctx, id)
	if err != nil {
		return FileServerAddr{}, fmt.Errorf("locator: metadata lookup: %w", err)
	}
	if repo == nil {
		return FileServerAddr{}, ErrRepoNotFound
	}
	return FileServerAddr{
		ReplicaSetID: repo.ReplicaSetID,
		HomeRegion:   repo.HomeRegion,
		Addr:         repo.ReplicaSetID + ":8075",
	}, nil
}
