package prnoise

import (
	"context"
	"errors"
)

// Service is the application-plane facade over the PR noise pipeline.
// Production code receives a PostgresService; tests and upstream
// packages may inject a StubService.
//
// RecordDecision MUST be all-or-nothing: either the decision row AND
// the pr.noise_decision_recorded outbox row both commit, or neither
// does (ADR-008). Implementations that cannot guarantee this contract
// MUST NOT satisfy this interface.
type Service interface {
	// RecordDecision runs the pipeline against in, persists the resulting
	// Decision, and writes the matching outbox row in the same Tx.
	// Returns the persisted Decision (with DecidedAt set if the input
	// did not provide it).
	RecordDecision(ctx context.Context, in PRInput) (Decision, error)
}

// ErrPipelineUnconfigured is returned when a Service is constructed
// without a Pipeline. The constructors guard against this; the error
// exists for defensive paths.
var ErrPipelineUnconfigured = errors.New("prnoise: pipeline is not configured")
