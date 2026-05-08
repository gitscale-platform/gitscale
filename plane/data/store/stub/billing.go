package stub

import (
	"context"
	"fmt"

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
