// Package metering implements the ADR-015 two-tier counter:
//
//   - Tier 1 (Redis, best-effort): an INCRBY-style counter keyed by
//     agent_id + billing window, used by the edge plane to make 429/throttle
//     decisions. A Redis blip MUST NOT reject a Git operation.
//
//   - Tier 2 (outbox, load-bearing): a row in git.git_outbox with the full
//     MeteringEvent payload. The existing per-domain outbox consumer drains
//     it to TopicGitMeteringEvents; the analytics sink (#109 stub today,
//     ClickHouse in Phase 3) is the durable reconciliation store. Failure
//     to write the outbox row is propagated to the caller, which rejects
//     the push (ADR-015).
//
// The Counter interface and NoopCounter constructor were introduced in
// PR #120 alongside the GitalyProxy bootstrap; this file adds the
// production TwoTierCounter without changing that interface.
package metering

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
	"github.com/google/uuid"
)

// Counter records a metering event for a completed Git operation.
// op is one of OpInfoRefs, OpUploadPack, OpReceivePack. bytes is the payload
// size in either direction; packObjects and refUpdates are receive-pack
// specific (zero on fetch paths).
//
// Implementations must be safe for concurrent use. A non-nil error from
// Record is propagated by the proxy and rejects the operation.
type Counter interface {
	Record(ctx context.Context, ref gittypes.RepoRef, op string, bytes int64, packObjects int64, refUpdates int) error
}

// noopCounter discards all events. Returned by NewNoopCounter.
type noopCounter struct{}

// NewNoopCounter returns a Counter that drops every event. Used as the
// default in tests and during bootstrap before the two-tier counter wires
// in at the application-plane edge.
func NewNoopCounter() Counter { return noopCounter{} }

func (noopCounter) Record(_ context.Context, _ gittypes.RepoRef, _ string, _ int64, _ int64, _ int) error {
	return nil
}

// EnforcementWindow is the bucket size for the Tier-1 Redis counter. The
// edge plane resets/expires keys at this granularity. One hour gives the
// reconciliation tier ample replay window without exploding key cardinality.
const EnforcementWindow = time.Hour

// EnforcementTTL is how long the Redis counter survives after the window
// closes. Two windows of slack covers clock skew and out-of-order updates
// arriving from different file-server replicas.
const EnforcementTTL = 2 * EnforcementWindow

// TwoTierCounter is the production Counter implementation. Construct with
// NewTwoTierCounter; pass a nil cache.CacheStore to disable Tier 1 (e.g. in
// tests or during a Redis outage — Tier 2 still records the event).
type TwoTierCounter struct {
	cache cache.CacheStore // nil-safe: enforcement tier is best-effort
	store store.MetadataStore
}

// NewTwoTierCounter constructs the two-tier counter. store is required;
// cacheStore is optional (nil means "skip Tier 1 silently").
func NewTwoTierCounter(cacheStore cache.CacheStore, mds store.MetadataStore) *TwoTierCounter {
	if mds == nil {
		// A Counter without an outbox is not safe — Tier 2 is load-bearing.
		// Crash early at construction rather than at Record() time.
		panic("metering: NewTwoTierCounter: store.MetadataStore is required")
	}
	return &TwoTierCounter{cache: cacheStore, store: mds}
}

// Record writes both tiers. Tier-1 errors (Redis) are swallowed; Tier-2
// errors (outbox) propagate.
//
// Aggregate identity: the outbox row's aggregate_id is derived from the
// repo_id when it parses as a UUID, so events for the same repo share a
// Kafka partition (ADR-004). When the repo_id is not a UUID — currently
// only the in-process tests — a fresh UUID is generated so the row still
// validates against the schema. EventID is always a fresh UUID and serves
// as the idempotency key.
func (t *TwoTierCounter) Record(
	ctx context.Context,
	ref gittypes.RepoRef,
	op string,
	bytes int64,
	packObjects int64,
	refUpdates int,
) error {
	now := time.Now().UTC()

	t.recordEnforcement(ctx, ref.AgentID, bytes, now)

	evt := MeteringEvent{
		EventID:          uuid.NewString(),
		AgentID:          ref.AgentID,
		RepoID:           ref.RepoID,
		Operation:        op,
		BytesTransferred: bytes,
		PackObjects:      packObjects,
		RefUpdates:       refUpdates,
		OccurredAt:       now,
		EnvelopeVersion:  1,
	}

	aggID := repoAggregateID(ref.RepoID)

	if err := t.store.Transact(ctx, func(tx store.Tx) error {
		return tx.WriteOutbox(ctx, store.DomainGit, AggregateType, aggID, EventType, evt)
	}); err != nil {
		return fmt.Errorf("metering: outbox write: %w", err)
	}
	return nil
}

// recordEnforcement implements the Tier-1 best-effort counter. Errors are
// silently dropped; the only contract is that a Redis failure must not
// reject a Git operation. AgentID == "" (human pushes) is skipped — there
// is no per-agent quota to enforce.
//
// The counter is read-modify-write rather than INCRBY because the
// CacheStore interface (ADR-009) does not expose INCRBY; this is acceptable
// for a best-effort tier where occasional racey overwrites cannot cross
// the 0.5% drift threshold (ADR-015) at production volume.
func (t *TwoTierCounter) recordEnforcement(ctx context.Context, agentID string, bytes int64, now time.Time) {
	if t.cache == nil || agentID == "" {
		return
	}
	window := now.Format("2006-01-02T15") // hourly bucket
	key := fmt.Sprintf("git:meter:%s:%s", agentID, window)

	var prev int64
	if existing, err := t.cache.Get(ctx, key); err == nil && len(existing) > 0 {
		// Stored as ASCII decimal so debugging via redis-cli shows a number.
		if v, perr := strconv.ParseInt(string(existing), 10, 64); perr == nil {
			prev = v
		}
	}
	updated := []byte(strconv.FormatInt(prev+bytes, 10))
	_ = t.cache.Set(ctx, key, updated, EnforcementTTL)
}

// repoAggregateID maps repo_id to a uuid.UUID for the outbox row. A repo_id
// that already parses as a UUID is reused so events for the same repo share
// a Kafka partition. Anything else gets a fresh UUID — only test fixtures
// take this path.
func repoAggregateID(repoID string) uuid.UUID {
	if id, err := uuid.Parse(repoID); err == nil {
		return id
	}
	return uuid.New()
}

