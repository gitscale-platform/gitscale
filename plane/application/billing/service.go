package billing

import (
	"context"
	"errors"
)

// Service is the billing domain service. RecordPartitionArchived performs
// source-row + outbox-row writes in a single Tx (ADR-008). State mutations
// are exclusive to this service in the application plane; the workflow plane
// reaches it through the gRPC surface (ADR-019).
type Service interface {
	RecordPartitionArchived(ctx context.Context, in RecordPartitionArchivedInput) (RecordPartitionArchivedOutput, error)
}

// Sentinel validation errors. Wrapped with %w-friendly equality at the gRPC
// layer where they map to InvalidArgument.
var (
	ErrInvalidYear        = errors.New("billing: year out of range (2026..2100)")
	ErrInvalidMonth       = errors.New("billing: month out of range (1..12)")
	ErrEmptyPartitionName = errors.New("billing: partition_name is empty")
	ErrEmptyLakeURI       = errors.New("billing: lake_uri is empty")
	ErrNegativeCount      = errors.New("billing: row_count or bytes_written is negative")
)

// validateInput enforces the contract documented on RecordPartitionArchivedInput.
// Validation runs before any Tx is opened so failed inputs never produce
// outbox rows.
func validateInput(in RecordPartitionArchivedInput) error {
	if in.Year < 2026 || in.Year > 2100 {
		return ErrInvalidYear
	}
	if in.Month < 1 || in.Month > 12 {
		return ErrInvalidMonth
	}
	if in.PartitionName == "" {
		return ErrEmptyPartitionName
	}
	if in.LakeURI == "" {
		return ErrEmptyLakeURI
	}
	if in.RowCount < 0 || in.BytesWritten < 0 {
		return ErrNegativeCount
	}
	return nil
}
