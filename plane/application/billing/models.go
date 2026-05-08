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
