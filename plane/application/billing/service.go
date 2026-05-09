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

	// RecordDEKDestroyed emits billing.partition_dek_destroyed to the outbox.
	// The DEK destruction itself is the source of truth (Vault transit key
	// version trim is irreversible); the outbox row is the audit record.
	// Idempotent on the (year, month, partition_name, kek_hint) tuple.
	RecordDEKDestroyed(ctx context.Context, in RecordDEKDestroyedInput) (RecordDEKDestroyedOutput, error)

	// GetQuotaAccount returns the org-level quota envelope used by CI boot
	// activities to enforce per-job ceilings (#110, ADR-019). Returns
	// ErrQuotaAccountNotFound when no row exists for the org.
	GetQuotaAccount(ctx context.Context, in GetQuotaAccountInput) (GetQuotaAccountOutput, error)

	// RecordCIJobCompleted writes the source row + ci.job_completed outbox
	// row in one Tx (#110, ADR-008/019). Idempotent on JobID — retries
	// return Created=false and do not double-write the outbox.
	RecordCIJobCompleted(ctx context.Context, in RecordCIJobCompletedInput) (RecordCIJobCompletedOutput, error)
}

// Sentinel validation errors. Wrapped with %w-friendly equality at the gRPC
// layer where they map to InvalidArgument.
var (
	ErrInvalidYear         = errors.New("billing: year out of range (2026..2100)")
	ErrInvalidMonth        = errors.New("billing: month out of range (1..12)")
	ErrEmptyPartitionName  = errors.New("billing: partition_name is empty")
	ErrEmptyLakeURI        = errors.New("billing: lake_uri is empty")
	ErrNegativeCount       = errors.New("billing: row_count or bytes_written is negative")
	ErrEmptyKEKHint        = errors.New("billing: kek_hint is empty")
	ErrInvalidKeyVersion   = errors.New("billing: vault_key_version must be > 0")

	// CI service errors (#110).
	ErrEmptyOrgID          = errors.New("billing: org_id is empty")
	ErrQuotaAccountNotFound = errors.New("billing: quota account not found for org")
	ErrEmptyJobID          = errors.New("billing: job_id is empty")
	ErrEmptyPrincipalID    = errors.New("billing: principal_id is empty")
	ErrInvalidPrincipalKind = errors.New("billing: principal_kind must be human|agent|service")
	ErrInvalidTier         = errors.New("billing: tier must be hot|cold")
	ErrNegativeMetric      = errors.New("billing: vcpu_seconds / memory_mb_seconds / egress_kb must be >= 0")
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

// validateDEKDestroyedInput enforces the contract on RecordDEKDestroyedInput.
// Validation runs before any Tx is opened so failed inputs never produce
// outbox rows.
func validateDEKDestroyedInput(in RecordDEKDestroyedInput) error {
	if in.Year < 2026 || in.Year > 2100 {
		return ErrInvalidYear
	}
	if in.Month < 1 || in.Month > 12 {
		return ErrInvalidMonth
	}
	if in.PartitionName == "" {
		return ErrEmptyPartitionName
	}
	if in.KEKHint == "" {
		return ErrEmptyKEKHint
	}
	if in.VaultKeyVersion < 1 {
		return ErrInvalidKeyVersion
	}
	return nil
}
