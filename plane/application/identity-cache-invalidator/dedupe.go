package invalidator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
)

// DedupeTTL must be ≥ Kafka retention for the topic so an event id seen at
// the start of the retention window cannot be re-processed at the tail. The
// gitscale.identity.events retention is 7d (plane/data/kafka/topics.yaml);
// 24h here is a starting point — bump if retention is widened.
const DedupeTTL = 24 * time.Hour

// dedupeKey returns the cache key for a given Kafka event_id. Stored under
// the existing identity cache namespace so a single Redis pod backs both
// hot identity entries and the dedupe markers.
func dedupeKey(eventID string) string {
	return fmt.Sprintf("identity:invalidator:dedupe:%s", eventID)
}

// Deduper is the minimal interface used by the consumer for idempotency.
// Production wiring is *cacheDeduper; tests inject a stub.
type Deduper interface {
	// Seen reports whether this eventID has been marked. Returns (false, err)
	// only on backend failure; a missing key is (false, nil).
	Seen(ctx context.Context, eventID string) (bool, error)
	// Mark records eventID as processed with a fixed TTL. Idempotent.
	Mark(ctx context.Context, eventID string) error
}

type cacheDeduper struct {
	store cache.CacheStore
}

// NewDeduper returns a Deduper backed by the cache.CacheStore. The marker
// payload is "1" — value content does not matter, only key presence.
func NewDeduper(store cache.CacheStore) Deduper {
	return &cacheDeduper{store: store}
}

func (d *cacheDeduper) Seen(ctx context.Context, eventID string) (bool, error) {
	_, err := d.store.Get(ctx, dedupeKey(eventID))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, cache.ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (d *cacheDeduper) Mark(ctx context.Context, eventID string) error {
	return d.store.Set(ctx, dedupeKey(eventID), []byte{'1'}, DedupeTTL)
}
