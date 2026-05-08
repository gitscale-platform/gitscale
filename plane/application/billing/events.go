package billing

import (
	"time"

	"github.com/google/uuid"
)

// EventTypePartitionArchived is the event_type written to billing.billing_outbox
// when a usage_events partition has been successfully archived to the data lake.
const EventTypePartitionArchived = "billing.partition_archived"

// EventTypePartitionDEKDestroyed is the event_type written to billing.billing_outbox
// after the DEK destruction workflow (#80) destroys a per-month Vault transit key
// version. Crypto-shred is irreversible — the outbox event is the audit record.
const EventTypePartitionDEKDestroyed = "billing.partition_dek_destroyed"

// envelopeVersion is the schema version of PartitionArchivedPayload. Bump when
// the payload shape changes in a way consumers need to disambiguate.
const envelopeVersion = 1

// PartitionArchivedPayload is the JSON payload written to the outbox. The
// _envelope_version field anchors forward-compatible consumer parsing.
type PartitionArchivedPayload struct {
	ArchiveID       uuid.UUID `json:"archive_id"`
	Year            int       `json:"year"`
	Month           int       `json:"month"`
	PartitionName   string    `json:"partition_name"`
	LakeURI         string    `json:"lake_uri"`
	RowCount        int64     `json:"row_count"`
	BytesWritten    int64     `json:"bytes_written"`
	ArchivedAt      time.Time `json:"archived_at"`
	EnvelopeVersion int       `json:"_envelope_version"`
}

// PartitionDEKDestroyedPayload is the JSON payload written to the outbox when
// a per-month DEK is crypto-shredded (#80). The payload pins the destroyed
// Vault transit key version (kek_hint + numeric vault_key_version) so audit
// consumers can correlate against the encrypted archive's manifest. The
// destruction is the source of truth: there is no row update on
// partition_archives — the event is the only durable record that destruction
// occurred.
type PartitionDEKDestroyedPayload struct {
	Year             int       `json:"year"`
	Month            int       `json:"month"`
	PartitionName    string    `json:"partition_name"`
	KEKHint          string    `json:"kek_hint"`
	VaultKeyVersion  int       `json:"vault_key_version"`
	DestroyedAt      time.Time `json:"destroyed_at"`
	EnvelopeVersion  int       `json:"_envelope_version"`
}

// newPartitionDEKDestroyedPayload builds the outbox payload for a destroyed
// per-month DEK. Only fields relevant to audit trail are included; key
// material is never logged or persisted.
func newPartitionDEKDestroyedPayload(in RecordDEKDestroyedInput, destroyedAt time.Time) PartitionDEKDestroyedPayload {
	return PartitionDEKDestroyedPayload{
		Year:            in.Year,
		Month:           in.Month,
		PartitionName:   in.PartitionName,
		KEKHint:         in.KEKHint,
		VaultKeyVersion: in.VaultKeyVersion,
		DestroyedAt:     destroyedAt,
		EnvelopeVersion: envelopeVersion,
	}
}

// newPartitionArchivedPayload builds the outbox payload from the persisted
// partition archive row. The caller must have already bound the canonical
// archive id into pa.ID.
func newPartitionArchivedPayload(pa PartitionArchive) PartitionArchivedPayload {
	return PartitionArchivedPayload{
		ArchiveID:       pa.ID,
		Year:            pa.Year,
		Month:           pa.Month,
		PartitionName:   pa.PartitionName,
		LakeURI:         pa.LakeURI,
		RowCount:        pa.RowCount,
		BytesWritten:    pa.BytesWritten,
		ArchivedAt:      pa.ArchivedAt,
		EnvelopeVersion: envelopeVersion,
	}
}
