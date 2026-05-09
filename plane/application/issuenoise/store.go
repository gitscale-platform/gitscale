package issuenoise

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// IssueState is the on-wire string for the issues row state column.
// Mirrors the CHECK-constraint values established by the migration:
// 'open', 'closed', 'held', 'auto_closed_spam'.
type IssueState string

const (
	// IssueStateOpen is the standard-queue admitted state.
	IssueStateOpen IssueState = "open"
	// IssueStateHeld is the maintainer-queue state for low_quality
	// and duplicate verdicts; subject to TTL via the Temporal
	// IssueHoldExpiryWorkflow.
	IssueStateHeld IssueState = "held"
	// IssueStateAutoClosedSpam is the terminal state for spam.
	IssueStateAutoClosedSpam IssueState = "auto_closed_spam"
	// IssueStateClosed is the maintainer/manual-close terminal state.
	IssueStateClosed IssueState = "closed"
)

// Decision is one row in the issue_noise_decisions audit table. The
// router writes a Decision in the same Tx as the issue state change
// and the outbox row.
type Decision struct {
	DecisionID    uuid.UUID
	IssueID       uuid.UUID
	RepoID        uuid.UUID
	ReporterID    uuid.UUID
	Verdict       Verdict
	ScorerVersion string
	ScoreSpam     float64
	ScoreLQ       float64
	ScoreDup      float64
	DuplicateOf   *uuid.UUID
	Signals       []Signal
	DecidedAt     time.Time
	DecidedBy     string // "auto" | "maintainer:<uuid>"
}

// Tx is a transaction handle the router holds across the in-flight
// Tx. Implementations bridge to a real pgx.Tx; tests use a stub. The
// type is intentionally opaque so callers cannot reach into the
// driver from the application plane (ADR-017).
type Tx interface {
	// AnchorIssue inserts or upserts the issues row with the given
	// state. The router calls this exactly once per Route invocation.
	AnchorIssue(ctx context.Context, d IssueDraft, state IssueState) error
	// SetIssueState mutates an existing issues row's state. Used by
	// Release and by the Temporal hold-expiry activity.
	SetIssueState(ctx context.Context, issueID uuid.UUID, state IssueState) error
	// InsertDecision writes one issue_noise_decisions row.
	InsertDecision(ctx context.Context, d Decision) error
	// WriteOutbox writes one collaboration_outbox row with the given
	// event_type and payload.
	WriteOutbox(ctx context.Context, eventType string, aggregateID uuid.UUID, payload any) error
}

// Store is the swap surface between the issuenoise router and the
// underlying metadata store. A real impl bridges to pgx; the in-tree
// stub for tests is in store_stub.go.
//
// Transact runs fn inside a serializable transaction; if fn returns
// nil the Tx commits. This shape mirrors store.MetadataStore.Transact
// without leaking pgx types into the application package.
type Store interface {
	Transact(ctx context.Context, fn func(Tx) error) error
}

// ThresholdsProvider returns the per-repo thresholds + hold TTL. A
// real impl reads issue_noise_config; an in-memory provider is used
// in tests and as a fallback when the config table is empty.
type ThresholdsProvider interface {
	Get(ctx context.Context, repoID uuid.UUID) (Thresholds, error)
}

// StaticThresholds is a ThresholdsProvider that always returns the
// platform defaults. Wired at boot until per-repo overrides exist.
type StaticThresholds struct{}

// Get returns DefaultThresholds.
func (StaticThresholds) Get(_ context.Context, _ uuid.UUID) (Thresholds, error) {
	return DefaultThresholds(), nil
}
