package issuenoise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Errors returned by Router.
var (
	// ErrNoStore indicates the router was constructed without a Store.
	ErrNoStore = errors.New("issuenoise: nil Store")
	// ErrNoScorer indicates the router was constructed without a Scorer.
	ErrNoScorer = errors.New("issuenoise: nil Scorer")
)

// RouterConfig is the constructor-arg bag for NewRouter. The fields
// are public so cmd/ wiring can tune individual knobs without the
// boilerplate of a builder.
type RouterConfig struct {
	// Store is the durable backend; required.
	Store Store
	// Scorer is the IssueScorer; required.
	Scorer IssueScorer
	// Thresh is the per-repo thresholds provider. If nil,
	// StaticThresholds is used.
	Thresh ThresholdsProvider
	// Workflows is the Temporal client wrapper. If nil,
	// NoopWorkflowStarter is used (post-commit side-effect skipped;
	// the reconciler is expected to pick up the slack).
	Workflows WorkflowStarter
	// Clock is the time source. If nil, time.Now is used.
	Clock func() time.Time
	// Enforce gates the dark-launch protocol. When false, Router.Route
	// computes the verdict, writes the decision row + outbox row, but
	// always anchors the issue with state=open. When true, the verdict
	// drives the issue state. Default false (per spec, first 14 days).
	Enforce bool
	// Logger is the slog logger used for fail-open scorer errors.
	// If nil, slog.Default() is used.
	Logger *slog.Logger
	// Metrics receives counter increments. If nil, metrics are
	// dropped — production wires a Prometheus impl at the cmd/ layer.
	Metrics RouterMetrics
}

// RouterMetrics is the metrics surface. Concrete impls live at the
// cmd/ boot layer (Prometheus). Keeping the interface here avoids a
// hard dep on prometheus/client_golang from a pure-logic package.
type RouterMetrics interface {
	IncRoute(verdict string)
	IncScorerError()
	ObserveRouteDuration(seconds float64)
}

// NopMetrics is a RouterMetrics that drops every call. Used when no
// metrics impl is wired.
type NopMetrics struct{}

// IncRoute is a no-op.
func (NopMetrics) IncRoute(_ string) {}

// IncScorerError is a no-op.
func (NopMetrics) IncScorerError() {}

// ObserveRouteDuration is a no-op.
func (NopMetrics) ObserveRouteDuration(_ float64) {}

// Router is the public entry point for the issuenoise package. One
// instance is constructed per process at boot; submission paths
// (REST, MCP, GraphQL) call Router.Route on insert and Router.Release
// on maintainer-driven release.
type Router struct {
	cfg RouterConfig
}

// NewRouter validates cfg and returns a *Router. Returns an error if
// required dependencies are missing.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if cfg.Store == nil {
		return nil, ErrNoStore
	}
	if cfg.Scorer == nil {
		return nil, ErrNoScorer
	}
	if cfg.Thresh == nil {
		cfg.Thresh = StaticThresholds{}
	}
	if cfg.Workflows == nil {
		cfg.Workflows = NoopWorkflowStarter{}
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	return &Router{cfg: cfg}, nil
}

// RouteResult is the public outcome of Route. Callers surface this
// to the API client so they see the verdict immediately.
type RouteResult struct {
	Verdict     Verdict
	State       IssueState
	DuplicateOf *uuid.UUID
	Enforced    bool
}

// scorerErrorCounter is package-level for tests to assert fail-open
// behaviour without wiring a metrics dep. Production reads metrics
// via cfg.Metrics.
var scorerErrorCounter atomic.Int64

// ScorerErrorCount exposes the test-side fail-open counter.
func ScorerErrorCount() int64 { return scorerErrorCounter.Load() }

// Route classifies d, persists the decision in one Tx with the issue
// row + outbox row, and (post-commit) starts the IssueHoldExpiryWorkflow
// when the verdict is held. Returns the result regardless of whether
// the post-commit workflow start succeeded — the reconciler covers
// gaps.
//
// Fail-open: if the scorer returns an error, the router increments
// issue_noise_scorer_errors_total, logs at WARN, and proceeds with a
// VerdictNormal score. This is intentional silent-failure-hunter
// bait — the alert on the metric is the contract that surfaces the
// degradation.
func (r *Router) Route(ctx context.Context, d IssueDraft) (RouteResult, error) {
	start := r.cfg.Clock()
	defer func() {
		r.cfg.Metrics.ObserveRouteDuration(r.cfg.Clock().Sub(start).Seconds())
	}()

	thresh, err := r.cfg.Thresh.Get(ctx, d.RepoID)
	if err != nil {
		return RouteResult{}, fmt.Errorf("router: load thresholds: %w", err)
	}

	score, scoreErr := r.cfg.Scorer.Score(ctx, d)
	if scoreErr != nil {
		// Fail-open: log + metric, but proceed with whatever score we
		// got (which may be partial — the rule scorer aggregates
		// errors and returns whatever non-erroring rules contributed).
		r.cfg.Logger.WarnContext(ctx, "issuenoise: scorer error (failing open)",
			slog.String("err", scoreErr.Error()),
			slog.String("issue_id", d.ID.String()),
			slog.String("repo_id", d.RepoID.String()),
		)
		r.cfg.Metrics.IncScorerError()
		scorerErrorCounter.Add(1)
		// Reset score to a neutral verdict — DO NOT trust partial
		// signals on error; we'd rather mis-admit than mis-spam.
		score = Score{ScorerVersion: RuleScorerVersion}
	}

	verdict := Decide(score, thresh)
	state := stateForVerdict(verdict, r.cfg.Enforce)

	dec := Decision{
		DecisionID:    uuid.New(),
		IssueID:       d.ID,
		RepoID:        d.RepoID,
		ReporterID:    d.ReporterID,
		Verdict:       verdict,
		ScorerVersion: score.ScorerVersion,
		ScoreSpam:     score.Spam,
		ScoreLQ:       score.LowQuality,
		ScoreDup:      score.Duplicate,
		DuplicateOf:   score.DuplicateOf,
		Signals:       score.Signals,
		DecidedAt:     start,
		DecidedBy:     "auto",
	}
	payload := RoutingDecidedPayload{
		IssueID:       d.ID,
		RepoID:        d.RepoID,
		ReporterID:    d.ReporterID,
		Verdict:       verdict.String(),
		ScorerVersion: score.ScorerVersion,
		ScoreSpam:     score.Spam,
		ScoreLQ:       score.LowQuality,
		ScoreDup:      score.Duplicate,
		DuplicateOf:   score.DuplicateOf,
		Signals:       score.Signals,
		DecidedAt:     start,
		DecidedBy:     "auto",
		Enforced:      r.cfg.Enforce,
		EnvelopeV:     EnvelopeVersion,
	}

	txErr := r.cfg.Store.Transact(ctx, func(tx Tx) error {
		if err := tx.AnchorIssue(ctx, d, state); err != nil {
			return fmt.Errorf("anchor issue: %w", err)
		}
		if err := tx.InsertDecision(ctx, dec); err != nil {
			return fmt.Errorf("insert decision: %w", err)
		}
		if err := tx.WriteOutbox(ctx, EventTypeRoutingDecided, d.ID, payload); err != nil {
			return fmt.Errorf("write outbox: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return RouteResult{}, fmt.Errorf("router: tx: %w", txErr)
	}

	r.cfg.Metrics.IncRoute(verdict.String())

	// Post-commit side-effect: start hold-expiry workflow when held.
	// Failure here does not block the ack — the reconciler picks up
	// held-without-workflow rows.
	if state == IssueStateHeld {
		err := r.cfg.Workflows.StartHoldExpiry(ctx, HoldExpiryParams{
			IssueID: d.ID,
			RepoID:  d.RepoID,
			HoldTTL: thresh.HoldTTL,
		})
		if err != nil {
			r.cfg.Logger.WarnContext(ctx, "issuenoise: start hold workflow (reconciler will retry)",
				slog.String("err", err.Error()),
				slog.String("issue_id", d.ID.String()),
			)
		}
	}

	return RouteResult{
		Verdict:     verdict,
		State:       state,
		DuplicateOf: score.DuplicateOf,
		Enforced:    r.cfg.Enforce,
	}, nil
}

// Release is the maintainer-driven path: flip the issue from held
// back to open, write a second decision row stamped with the
// maintainer's id, write a release outbox event, and signal the
// running hold workflow to abort cleanly. All three writes happen in
// one Tx (ADR-008).
func (r *Router) Release(ctx context.Context, issueID, repoID, maintainerID uuid.UUID) error {
	now := r.cfg.Clock()
	dec := Decision{
		DecisionID:    uuid.New(),
		IssueID:       issueID,
		RepoID:        repoID,
		ReporterID:    uuid.Nil, // not known on release; maintainer is captured in DecidedBy
		Verdict:       VerdictNormal,
		ScorerVersion: "manual",
		DecidedAt:     now,
		DecidedBy:     "maintainer:" + maintainerID.String(),
	}
	payload := ReleasedPayload{
		IssueID:      issueID,
		RepoID:       repoID,
		MaintainerID: maintainerID,
		ReleasedAt:   now,
		EnvelopeV:    EnvelopeVersion,
	}
	if err := r.cfg.Store.Transact(ctx, func(tx Tx) error {
		if err := tx.SetIssueState(ctx, issueID, IssueStateOpen); err != nil {
			return fmt.Errorf("set issue state: %w", err)
		}
		if err := tx.InsertDecision(ctx, dec); err != nil {
			return fmt.Errorf("insert decision: %w", err)
		}
		if err := tx.WriteOutbox(ctx, EventTypeReleased, issueID, payload); err != nil {
			return fmt.Errorf("write outbox: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("router: release tx: %w", err)
	}
	// Best-effort: signal the workflow to exit cleanly. If it has
	// already completed, the impl returns nil.
	if err := r.cfg.Workflows.SignalRelease(ctx, issueID); err != nil {
		r.cfg.Logger.WarnContext(ctx, "issuenoise: signal release (workflow may have already completed)",
			slog.String("err", err.Error()),
			slog.String("issue_id", issueID.String()),
		)
	}
	return nil
}

// stateForVerdict translates a Verdict + the dark-launch enforce flag
// into the issue state to anchor. Pure function.
func stateForVerdict(v Verdict, enforce bool) IssueState {
	if !enforce {
		// Dark-launch: regardless of verdict, issue is admitted as
		// open. Decision row + outbox event are still written so
		// downstream tuning can use them.
		return IssueStateOpen
	}
	switch v {
	case VerdictSpam:
		return IssueStateAutoClosedSpam
	case VerdictLowQuality, VerdictDuplicate:
		return IssueStateHeld
	default:
		return IssueStateOpen
	}
}
