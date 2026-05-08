package billing

import "github.com/gitscale-platform/gitscale/plane/data/store"

// PartitionArchive is the application-layer view of a billing.partition_archives
// row. Aliased to the storage struct; same convention as identity.HumanUser.
type PartitionArchive = store.PartitionArchive

// RecordPartitionArchivedInput is the service-level input for the RPC of the
// same name. It is the shape used by both the in-process call and the gRPC
// entry point (after proto -> struct translation).
type RecordPartitionArchivedInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
}

// RecordPartitionArchivedOutput is the service-level output. Created is false
// on idempotent retry (the natural key already had a row).
type RecordPartitionArchivedOutput struct {
	ArchiveID string // UUID stringified
	Created   bool
}

// RecordDEKDestroyedInput is the service-level input recording an irreversible
// per-month DEK destruction (#80). KEKHint is the manifest hint
// ("platform-billing-v<N>") and VaultKeyVersion is the parsed numeric N.
// PartitionName mirrors billing.partition_archives.partition_name for the
// row whose ciphertext is now unrecoverable.
type RecordDEKDestroyedInput struct {
	Year            int
	Month           int
	PartitionName   string
	KEKHint         string
	VaultKeyVersion int
}

// RecordDEKDestroyedOutput is the service-level output. Created is false on
// idempotent retry (the outbox row for this (year,month,partition_name,
// kek_hint) tuple was already emitted).
type RecordDEKDestroyedOutput struct {
	EventID string // UUID stringified
	Created bool
}
