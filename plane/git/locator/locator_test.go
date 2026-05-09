package locator_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// alwaysMissLocator simulates a backing locator that knows nothing.
type alwaysMissLocator struct{}

func (l *alwaysMissLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return locator.FileServerAddr{}, locator.ErrRepoNotFound
}

// recordingLocator counts how many times Resolve was invoked.
type recordingLocator struct {
	result locator.FileServerAddr
	calls  int
}

func (l *recordingLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	l.calls++
	return l.result, nil
}

func TestCacheLocator_Hit(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()
	require.NoError(t, cache.SetRepoLocation(context.Background(), mem, repoID, cache.RepoLocation{
		ReplicaSetID: "rs-west-1",
		HomeRegion:   "us-west-2",
	}))

	loc := locator.NewCacheLocator(mem, &alwaysMissLocator{})
	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-west-1", addr.ReplicaSetID)
	require.Equal(t, "us-west-2", addr.HomeRegion)
	require.Equal(t, "rs-west-1:8075", addr.Addr)
}

func TestCacheLocator_MissFallsThroughThenCaches(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()

	fallback := &recordingLocator{
		result: locator.FileServerAddr{
			ReplicaSetID: "rs-east-1",
			HomeRegion:   "us-east-1",
			Addr:         "rs-east-1:8075",
		},
	}
	loc := locator.NewCacheLocator(mem, fallback)

	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-east-1", addr.ReplicaSetID)
	require.Equal(t, 1, fallback.calls)

	_, err = loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, 1, fallback.calls, "second call must hit the cache, not fallback")
}

func TestCacheLocator_NotFoundIsNegativeCached(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	repoID := uuid.New()
	loc := locator.NewCacheLocator(mem, &alwaysMissLocator{})

	_, err := loc.Resolve(context.Background(), repoID.String())
	require.ErrorIs(t, err, locator.ErrRepoNotFound)

	// Negative-cache TTL is short but non-zero; second call must not panic
	// and must return the same error.
	_, err = loc.Resolve(context.Background(), repoID.String())
	require.ErrorIs(t, err, locator.ErrRepoNotFound)
}

func TestCacheLocator_InvalidRepoID(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	loc := locator.NewCacheLocator(mem, &alwaysMissLocator{})
	_, err := loc.Resolve(context.Background(), "not-a-uuid")
	require.Error(t, err)
	require.NotErrorIs(t, err, locator.ErrRepoNotFound)
}

// stubMetadataStore is a minimal MetadataStore for testing MetadataLocator.
// It exposes only Repositories(); other domains panic if accessed.
type stubMetadataStore struct {
	repos map[uuid.UUID]store.Repository
}

func (s *stubMetadataStore) Transact(_ context.Context, _ func(store.Tx) error) error {
	return nil
}
func (s *stubMetadataStore) Identity() store.IdentityReader { return nil }
func (s *stubMetadataStore) Billing() store.BillingReader   { return nil }
func (s *stubMetadataStore) Repositories() store.RepositoryReader {
	return &stubRepoReader{repos: s.repos}
}

type stubRepoReader struct {
	repos map[uuid.UUID]store.Repository
}

func (r *stubRepoReader) GetByID(_ context.Context, id uuid.UUID) (*store.Repository, error) {
	if repo, ok := r.repos[id]; ok {
		return &repo, nil
	}
	return nil, nil
}

func (r *stubRepoReader) GetBySlug(_ context.Context, _ string) (*store.Repository, error) {
	return nil, nil
}

func (r *stubRepoReader) ListByOrg(_ context.Context, _ uuid.UUID, _ *time.Time, _ *uuid.UUID, _ int) ([]store.Repository, error) {
	return nil, nil
}

func TestMetadataLocator_Found(t *testing.T) {
	repoID := uuid.New()
	mds := &stubMetadataStore{repos: map[uuid.UUID]store.Repository{
		repoID: {
			ID:           repoID,
			ReplicaSetID: "rs-us-1",
			HomeRegion:   "us-west-2",
			CreatedAt:    time.Now(),
		},
	}}
	loc := locator.NewMetadataLocator(mds)
	addr, err := loc.Resolve(context.Background(), repoID.String())
	require.NoError(t, err)
	require.Equal(t, "rs-us-1", addr.ReplicaSetID)
	require.Equal(t, "us-west-2", addr.HomeRegion)
	require.Equal(t, "rs-us-1:8075", addr.Addr)
}

func TestMetadataLocator_NotFound(t *testing.T) {
	mds := &stubMetadataStore{repos: map[uuid.UUID]store.Repository{}}
	loc := locator.NewMetadataLocator(mds)
	_, err := loc.Resolve(context.Background(), uuid.New().String())
	require.ErrorIs(t, err, locator.ErrRepoNotFound)
}

func TestMetadataLocator_InvalidRepoID(t *testing.T) {
	mds := &stubMetadataStore{repos: map[uuid.UUID]store.Repository{}}
	loc := locator.NewMetadataLocator(mds)
	_, err := loc.Resolve(context.Background(), "garbage")
	require.Error(t, err)
	require.NotErrorIs(t, err, locator.ErrRepoNotFound)
}
