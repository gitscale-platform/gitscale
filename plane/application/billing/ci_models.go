package billing

import (
	"time"

	"github.com/google/uuid"
)

// EventTypeCIJobCompleted is the event_type written to billing.billing_outbox
// when a CI job finishes (#110). Idempotent on JobID — repeat emits dedupe
// on the deterministic aggregate id.
const EventTypeCIJobCompleted = "ci.job_completed"

// GetQuotaAccountInput is the service-level input for the RPC of the same
// name (#110, ADR-019).
type GetQuotaAccountInput struct {
	OrgID uuid.UUID
}

// GetQuotaAccountOutput is the service-level output. Caps are per-period —
// the boot activity derives the per-job ceiling from the monthly compute
// minutes cap.
type GetQuotaAccountOutput struct {
	AccountID                 uuid.UUID
	OrgID                     uuid.UUID
	PlanTier                  string
	TokensPerWeekCap          int64
	ComputeMinutesPerMonthCap int64
	StorageGBCap              int64
}

// RecordCIJobCompletedInput is the service-level input. The application
// service writes the source row + outbox row in one Tx (ADR-008/019).
type RecordCIJobCompletedInput struct {
	JobID           uuid.UUID
	PrincipalID     uuid.UUID
	PrincipalKind   string // "human" | "agent" | "service"
	OrgID           uuid.UUID
	RepoID          uuid.UUID
	Tier            string // "hot" | "cold"
	VCPUSeconds     float64
	MemoryMBSeconds float64
	EgressKB        int64
	ExitCode        int
}

// RecordCIJobCompletedOutput is the service-level output. EventID is the
// deterministic UUIDv5 derived from JobID; Created is false on idempotent
// retry.
type RecordCIJobCompletedOutput struct {
	EventID string
	Created bool
}

// CIJobCompletedPayload is the JSON payload written to the outbox. Mirrors
// the proto/grpc shape one-for-one so downstream consumers see consistent
// field names regardless of which producer emitted.
type CIJobCompletedPayload struct {
	EventID         uuid.UUID `json:"event_id"`
	JobID           uuid.UUID `json:"job_id"`
	PrincipalID     uuid.UUID `json:"principal_id"`
	PrincipalKind   string    `json:"principal_kind"`
	OrgID           uuid.UUID `json:"org_id"`
	RepoID          uuid.UUID `json:"repo_id"`
	Tier            string    `json:"tier"`
	VCPUSeconds     float64   `json:"vcpu_seconds"`
	MemoryMBSeconds float64   `json:"memory_mb_seconds"`
	EgressKB        int64     `json:"egress_kb"`
	ExitCode        int       `json:"exit_code"`
	OccurredAt      time.Time `json:"occurred_at"`
	EnvelopeVersion int       `json:"_envelope_version"`
}

// validateCIJobInput enforces the contract on RecordCIJobCompletedInput.
// Validation runs before any Tx is opened.
func validateCIJobInput(in RecordCIJobCompletedInput) error {
	if in.JobID == uuid.Nil {
		return ErrEmptyJobID
	}
	if in.PrincipalID == uuid.Nil {
		return ErrEmptyPrincipalID
	}
	if in.OrgID == uuid.Nil {
		return ErrEmptyOrgID
	}
	switch in.PrincipalKind {
	case "human", "agent", "service":
	default:
		return ErrInvalidPrincipalKind
	}
	switch in.Tier {
	case "hot", "cold":
	default:
		return ErrInvalidTier
	}
	if in.VCPUSeconds < 0 || in.MemoryMBSeconds < 0 || in.EgressKB < 0 {
		return ErrNegativeMetric
	}
	return nil
}

// ciJobCompletedNamespace anchors the deterministic UUID derivation for the
// outbox aggregate_id of a ci.job_completed event. Stable across versions;
// bump only when changing the natural-key shape.
var ciJobCompletedNamespace = uuid.MustParse("3f4e5d6c-7b8a-4998-8b7c-6d5e4f3a2b1c")

// ciJobCompletedAggregateID derives a deterministic UUIDv5 from JobID.
// Same JobID always yields the same aggregate id, which lets
// HasOutboxEventForAggregate anchor idempotency.
func ciJobCompletedAggregateID(jobID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(ciJobCompletedNamespace, jobID[:])
}

// newCIJobCompletedPayload builds the outbox payload from the input + a
// generated event_id. occurredAt is sourced from the service clock.
func newCIJobCompletedPayload(in RecordCIJobCompletedInput, eventID uuid.UUID, occurredAt time.Time) CIJobCompletedPayload {
	return CIJobCompletedPayload{
		EventID:         eventID,
		JobID:           in.JobID,
		PrincipalID:     in.PrincipalID,
		PrincipalKind:   in.PrincipalKind,
		OrgID:           in.OrgID,
		RepoID:          in.RepoID,
		Tier:            in.Tier,
		VCPUSeconds:     in.VCPUSeconds,
		MemoryMBSeconds: in.MemoryMBSeconds,
		EgressKB:        in.EgressKB,
		ExitCode:        in.ExitCode,
		OccurredAt:      occurredAt,
		EnvelopeVersion: envelopeVersion,
	}
}
