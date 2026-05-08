package stub

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// partitionArchiveKey returns the natural-key string used to index the
// in-memory partition_archives map. Mirrors the UNIQUE(year, month,
// partition_name) constraint enforced by the postgres impl.
func partitionArchiveKey(year, month int, partitionName string) string {
	return fmt.Sprintf("%d-%d-%s", year, month, partitionName)
}

// Billing returns a reader backed by the committed in-memory archives map.
func (s *Store) Billing() store.BillingReader {
	return &stubBillingReader{store: s}
}

// stubBillingReader serves reads from committed state.
type stubBillingReader struct {
	store *Store
}

func (r *stubBillingReader) GetPartitionArchiveByKey(_ context.Context, year, month int, partitionName string) (*store.PartitionArchive, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	pa, ok := r.store.partitionArchives[partitionArchiveKey(year, month, partitionName)]
	if !ok {
		return nil, nil
	}
	cp := pa
	return &cp, nil
}

// ListPartitionArchivesArchivedBefore returns committed archive rows older
// than cutoff, sorted to mirror the postgres impl's ORDER BY year, month, id.
func (r *stubBillingReader) ListPartitionArchivesArchivedBefore(_ context.Context, cutoff time.Time) ([]store.PartitionArchive, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	var out []store.PartitionArchive
	for _, pa := range r.store.partitionArchives {
		if pa.ArchivedAt.Before(cutoff) {
			out = append(out, pa)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Year != out[j].Year {
			return out[i].Year < out[j].Year
		}
		if out[i].Month != out[j].Month {
			return out[i].Month < out[j].Month
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

// HasOutboxEventForAggregate consults committed outbox records. Mirrors the
// postgres impl's WHERE event_type=$1 AND aggregate_id=$2 query. Pending
// (uncommitted) writes are checked separately by stubBillingWriter.
func (r *stubBillingReader) HasOutboxEventForAggregate(_ context.Context, eventType string, aggregateID uuid.UUID) (bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, ev := range r.store.outbox {
		if ev.Domain == store.DomainBilling && ev.EventType == eventType && ev.AggregateID == aggregateID {
			return true, nil
		}
	}
	return false, nil
}

// Billing returns the billing writer scoped to this Tx. Pending writes are
// only made visible on commit; reads consult both pending and committed maps.
func (t *stubTx) Billing() store.BillingWriter {
	return &stubBillingWriter{tx: t, reader: &stubBillingReader{store: t.store}}
}

type stubBillingWriter struct {
	tx     *stubTx
	reader *stubBillingReader
}

func (w *stubBillingWriter) lazyInit() {
	if w.tx.pendingPartitionArchives == nil {
		w.tx.pendingPartitionArchives = make(map[string]store.PartitionArchive)
	}
}

// ListPartitionArchivesArchivedBefore consults pending writes first, then
// falls back to committed state. Sort order matches the reader's.
func (w *stubBillingWriter) ListPartitionArchivesArchivedBefore(ctx context.Context, cutoff time.Time) ([]store.PartitionArchive, error) {
	committed, err := w.reader.ListPartitionArchivesArchivedBefore(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	if w.tx.pendingPartitionArchives == nil {
		return committed, nil
	}
	merged := append([]store.PartitionArchive(nil), committed...)
	for _, pa := range w.tx.pendingPartitionArchives {
		if pa.ArchivedAt.Before(cutoff) {
			merged = append(merged, pa)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Year != merged[j].Year {
			return merged[i].Year < merged[j].Year
		}
		if merged[i].Month != merged[j].Month {
			return merged[i].Month < merged[j].Month
		}
		return merged[i].ID.String() < merged[j].ID.String()
	})
	return merged, nil
}

// HasOutboxEventForAggregate checks pending Tx writes first, then committed
// outbox state. The pending check uses the same filter as the postgres impl.
func (w *stubBillingWriter) HasOutboxEventForAggregate(ctx context.Context, eventType string, aggregateID uuid.UUID) (bool, error) {
	for _, ev := range w.tx.pendingOutbox {
		if ev.Domain == store.DomainBilling && ev.EventType == eventType && ev.AggregateID == aggregateID {
			return true, nil
		}
	}
	return w.reader.HasOutboxEventForAggregate(ctx, eventType, aggregateID)
}

func (w *stubBillingWriter) GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*store.PartitionArchive, error) {
	key := partitionArchiveKey(year, month, partitionName)
	if w.tx.pendingPartitionArchives != nil {
		if pa, ok := w.tx.pendingPartitionArchives[key]; ok {
			cp := pa
			return &cp, nil
		}
	}
	return w.reader.GetPartitionArchiveByKey(ctx, year, month, partitionName)
}

// InsertPartitionArchiveIfAbsent enforces the same UNIQUE-key contract as
// the postgres impl: a duplicate natural key returns the existing id with
// created=false and does not overwrite.
func (w *stubBillingWriter) InsertPartitionArchiveIfAbsent(ctx context.Context, pa store.PartitionArchive) (uuid.UUID, bool, error) {
	w.lazyInit()
	key := partitionArchiveKey(pa.Year, pa.Month, pa.PartitionName)

	// Pending writes within this Tx win over committed reads.
	if existing, ok := w.tx.pendingPartitionArchives[key]; ok {
		return existing.ID, false, nil
	}

	// Committed state from prior Tx.
	existing, err := w.reader.GetPartitionArchiveByKey(ctx, pa.Year, pa.Month, pa.PartitionName)
	if err != nil {
		return uuid.Nil, false, err
	}
	if existing != nil {
		return existing.ID, false, nil
	}

	w.tx.pendingPartitionArchives[key] = pa
	return pa.ID, true, nil
}
