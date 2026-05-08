package billing

import (
	"time"

	"github.com/google/uuid"
)

// EventTypePartitionArchived is the event_type written to billing.billing_outbox
// when a usage_events partition has been successfully archived to the data lake.
const EventTypePartitionArchived = "billing.partition_archived"

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
