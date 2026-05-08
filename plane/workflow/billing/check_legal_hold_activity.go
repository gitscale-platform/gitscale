package billing

import (
	"context"
	"errors"
)

// ActivityNameCheckLegalHold is the registered name for the
// CheckLegalHoldActivity.Execute method.
const ActivityNameCheckLegalHold = "billing.CheckLegalHoldForDEKDestroy"

// LegalHoldCheckInput identifies the partition to check.
type LegalHoldCheckInput struct {
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
}

// LegalHoldCheckResult reports whether the partition is currently held.
// Reason is non-empty when Held is true and is propagated as the workflow
// skip reason for the partition.
type LegalHoldCheckResult struct {
	Held   bool
	Reason string
}

// LegalHoldChecker is the workflow-plane abstraction over the underlying
// legal-hold mechanism (S3 Object Lock GetObjectLockConfiguration, a
// dedicated metadata table, etc.). The spec D9 deliberately leaves the
// concrete mechanism out of scope for #80; production wires whichever
// implementation lands first.
type LegalHoldChecker interface {
	IsHeld(ctx context.Context, lakeURI string) (held bool, reason string, err error)
}

// CheckLegalHoldActivity wraps a LegalHoldChecker so the workflow can call
// the boundary as a Temporal activity (network access, etc. are confined
// here per ADR-003).
type CheckLegalHoldActivity struct {
	checker LegalHoldChecker
}

// NewCheckLegalHoldActivity wraps checker.
func NewCheckLegalHoldActivity(checker LegalHoldChecker) (*CheckLegalHoldActivity, error) {
	if checker == nil {
		return nil, errors.New("billing.NewCheckLegalHoldActivity: checker is nil")
	}
	return &CheckLegalHoldActivity{checker: checker}, nil
}

// Execute returns the legal-hold state for the partition's lake URI.
func (a *CheckLegalHoldActivity) Execute(ctx context.Context, in LegalHoldCheckInput) (LegalHoldCheckResult, error) {
	held, reason, err := a.checker.IsHeld(ctx, in.LakeURI)
	if err != nil {
		return LegalHoldCheckResult{}, err
	}
	return LegalHoldCheckResult{Held: held, Reason: reason}, nil
}

// staticLegalHoldChecker returns the same answer for every call. Used as a
// safe default when no real checker is wired (production must replace it
// with an actual S3-Object-Lock-backed implementation).
type staticLegalHoldChecker struct {
	held   bool
	reason string
}

// NewStaticLegalHoldChecker returns a LegalHoldChecker that always returns
// (held, reason). Useful for development environments before the real S3
// integration ships and for unit tests.
func NewStaticLegalHoldChecker(held bool, reason string) LegalHoldChecker {
	return staticLegalHoldChecker{held: held, reason: reason}
}

// IsHeld implements LegalHoldChecker.
func (s staticLegalHoldChecker) IsHeld(_ context.Context, _ string) (bool, string, error) {
	return s.held, s.reason, nil
}
