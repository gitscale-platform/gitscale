package billing

import (
	"context"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// PostgresService implements Service against any store.MetadataStore. The
// constructor takes the interface so cmd/billing-service can inject a
// pgxpool-backed store and tests can inject the in-memory stub.
//
// RecordPartitionArchived opens exactly one Transact and emits the source
// row + outbox row in the same Tx (ADR-008). Idempotency is anchored on
// UNIQUE(year, month, partition_name) at the DB level: only the first
// successful insert produces an outbox event; retries return the existing
// archive id with Created=false.
type PostgresService struct {
	store store.MetadataStore
	clock func() time.Time
}

// NewPostgresService wraps ms.
func NewPostgresService(ms store.MetadataStore) *PostgresService {
	return &PostgresService{
		store: ms,
		clock: func() time.Time { return time.Now().UTC() },
	}
}

// RecordPartitionArchived persists a billing.partition_archives row and emits
// billing.partition_archived to the outbox in the same Tx. Validation runs
// before the Tx is opened so failed inputs never reach the DB.
func (s *PostgresService) RecordPartitionArchived(ctx context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error) {
	if err := validateInput(in); err != nil {
		return RecordPartitionArchivedOutput{}, err
	}

	candidate := PartitionArchive{
		ID:            uuid.New(),
		Year:          in.Year,
		Month:         in.Month,
		PartitionName: in.PartitionName,
		LakeURI:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
		ArchivedAt:    s.clock(),
	}

	var (
		archiveID uuid.UUID
		created   bool
	)
	err := s.store.Transact(ctx, func(tx store.Tx) error {
		id, ok, err := tx.Billing().InsertPartitionArchiveIfAbsent(ctx, candidate)
		if err != nil {
			return err
		}
		archiveID = id
		created = ok
		if !created {
			// Idempotent retry: the original Tx already wrote the outbox row.
			// Writing another would violate exactly-one-event-per-archive.
			return nil
		}
		// Bind the canonical id (matches the DB row) into the payload before
		// serialising so consumers see a stable ArchiveID across retries.
		stored := candidate
		stored.ID = id
		return tx.WriteOutbox(
			ctx,
			store.DomainBilling,
			"partition_archive",
			id,
			EventTypePartitionArchived,
			newPartitionArchivedPayload(stored),
		)
	})
	if err != nil {
		return RecordPartitionArchivedOutput{}, err
	}
	return RecordPartitionArchivedOutput{ArchiveID: archiveID.String(), Created: created}, nil
}
