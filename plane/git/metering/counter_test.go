package metering_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	stubstore "github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestTwoTierCounter_BothTiersWritten exercises the happy path: a successful
// Record writes the Tier-1 Redis counter (under the agent + window key) and
// a single Tier-2 outbox row in the git domain.
func TestTwoTierCounter_BothTiersWritten(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	mds := stubstore.New()
	counter := metering.NewTwoTierCounter(mem, mds)

	repoID := uuid.NewString()
	ref := gittypes.RepoRef{RepoID: repoID, AgentID: "agent-1"}
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 1024, 10, 2))

	// Tier 1: Redis enforcement counter holds the byte total under the
	// agent-scoped key for the current hourly window.
	window := time.Now().UTC().Format("2006-01-02T15")
	key := "git:meter:agent-1:" + window
	val, err := mem.Get(context.Background(), key)
	require.NoError(t, err, "enforcement counter must be in cache")
	got, err := strconv.ParseInt(string(val), 10, 64)
	require.NoError(t, err)
	require.Equal(t, int64(1024), got)

	// Tier 2: exactly one outbox row in the git domain.
	rec := filterDomain(mds.Recorded(), store.DomainGit)
	require.Len(t, rec, 1)
	require.Equal(t, metering.EventType, rec[0].EventType)
	require.Equal(t, metering.AggregateType, rec[0].AggregateType)

	evt, ok := rec[0].Payload.(metering.MeteringEvent)
	require.True(t, ok, "payload must be MeteringEvent, got %T", rec[0].Payload)
	require.Equal(t, repoID, evt.RepoID)
	require.Equal(t, "agent-1", evt.AgentID)
	require.Equal(t, metering.OpReceivePack, evt.Operation)
	require.Equal(t, int64(1024), evt.BytesTransferred)
	require.Equal(t, int64(10), evt.PackObjects)
	require.Equal(t, 2, evt.RefUpdates)
	require.Equal(t, 1, evt.EnvelopeVersion)
	require.NotEmpty(t, evt.EventID)
}

// TestTwoTierCounter_AggregateIDMatchesRepoUUID verifies the partition-key
// invariant: when repo_id parses as a UUID, the outbox aggregate_id is that
// UUID, so events for the same repo share a Kafka partition (ADR-004).
func TestTwoTierCounter_AggregateIDMatchesRepoUUID(t *testing.T) {
	mds := stubstore.New()
	counter := metering.NewTwoTierCounter(nil, mds)

	repoID := uuid.New()
	ref := gittypes.RepoRef{RepoID: repoID.String(), AgentID: "agent-1"}
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 0, 0, 0))

	rec := filterDomain(mds.Recorded(), store.DomainGit)
	require.Len(t, rec, 1)
	require.Equal(t, repoID, rec[0].AggregateID)
}

// TestTwoTierCounter_RedisFailDoesNotBlockOutbox simulates Redis being
// unavailable (nil CacheStore). Tier 1 silently skips; Tier 2 still writes.
func TestTwoTierCounter_RedisFailDoesNotBlockOutbox(t *testing.T) {
	mds := stubstore.New()
	counter := metering.NewTwoTierCounter(nil, mds)

	ref := gittypes.RepoRef{RepoID: uuid.NewString(), AgentID: "agent-1"}
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 512, 5, 1))
	require.Len(t, filterDomain(mds.Recorded(), store.DomainGit), 1)
}

// TestTwoTierCounter_HumanPushSkipsEnforcement verifies that operations with
// no agent_id (human pushes) bypass the Tier-1 Redis write entirely — there
// is no per-agent quota to enforce. Tier 2 still records the event so
// reconciliation totals stay accurate.
func TestTwoTierCounter_HumanPushSkipsEnforcement(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	mds := stubstore.New()
	counter := metering.NewTwoTierCounter(mem, mds)

	ref := gittypes.RepoRef{RepoID: uuid.NewString(), AgentID: ""}
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 1, 0, 0))

	// No key with empty agent_id should exist.
	window := time.Now().UTC().Format("2006-01-02T15")
	_, err := mem.Get(context.Background(), "git:meter::"+window)
	require.ErrorIs(t, err, cache.ErrNotFound)

	require.Len(t, filterDomain(mds.Recorded(), store.DomainGit), 1)
}

// TestTwoTierCounter_OutboxFailPropagates verifies the Tier-2 contract: an
// outbox failure must propagate to the caller so the proxy rejects the
// push (ADR-015 — metering integrity).
func TestTwoTierCounter_OutboxFailPropagates(t *testing.T) {
	counter := metering.NewTwoTierCounter(nil, &failingMetadataStore{})
	ref := gittypes.RepoRef{RepoID: uuid.NewString(), AgentID: "agent-1"}
	err := counter.Record(context.Background(), ref, metering.OpReceivePack, 0, 0, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outbox")
}

// TestTwoTierCounter_EnforcementAccumulates verifies repeated Records within
// the same window read the previous value and sum, not overwrite. Two
// records of 100 bytes each must leave 200 in the cache.
func TestTwoTierCounter_EnforcementAccumulates(t *testing.T) {
	mem := cache.NewMemoryStore(nil)
	counter := metering.NewTwoTierCounter(mem, stubstore.New())

	ref := gittypes.RepoRef{RepoID: uuid.NewString(), AgentID: "agent-acc"}
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 100, 0, 0))
	require.NoError(t, counter.Record(context.Background(), ref, metering.OpReceivePack, 100, 0, 0))

	window := time.Now().UTC().Format("2006-01-02T15")
	val, err := mem.Get(context.Background(), "git:meter:agent-acc:"+window)
	require.NoError(t, err)
	require.Equal(t, "200", string(val))
}

// TestNewTwoTierCounter_NilStorePanics asserts the constructor refuses a nil
// MetadataStore: a Counter without an outbox cannot satisfy ADR-015.
func TestNewTwoTierCounter_NilStorePanics(t *testing.T) {
	require.PanicsWithValue(
		t,
		"metering: NewTwoTierCounter: store.MetadataStore is required",
		func() { _ = metering.NewTwoTierCounter(cache.NewMemoryStore(nil), nil) },
	)
}

// filterDomain isolates outbox records for the given domain — the stub
// store interleaves rows from every Record call, including any future
// non-git tests that share the same fixture.
func filterDomain(all []stubstore.OutboxRecord, d store.Domain) []stubstore.OutboxRecord {
	var out []stubstore.OutboxRecord
	for _, r := range all {
		if r.Domain == d {
			out = append(out, r)
		}
	}
	return out
}

// failingMetadataStore.Transact always errors. Used to verify the Tier-2
// failure path. Other readers return nil — Record never calls them.
type failingMetadataStore struct{}

func (f *failingMetadataStore) Transact(_ context.Context, _ func(store.Tx) error) error {
	return errors.New("db: connection lost")
}
func (f *failingMetadataStore) Identity() store.IdentityReader       { return nil }
func (f *failingMetadataStore) Repositories() store.RepositoryReader { return nil }
func (f *failingMetadataStore) Billing() store.BillingReader         { return nil }
