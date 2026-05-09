package prnoise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgxConnector is the minimum surface PostgresService needs from a pgx
// pool. Keeping the dependency this small lets tests inject a fake (or
// the integration suite use a real *pgxpool.Pool) without binding the
// package to a concrete pgxpool import — preserving the ADR-017 swap
// surface at the application-plane edge.
type PgxConnector interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// retryMaxAttempts bounds the number of times RecordDecision will
// reissue the Tx after a 40001 serialization failure. Mirrors the
// identity package's retry budget for consistency.
const retryMaxAttempts = 3

// ErrRetryExhausted is returned when RecordDecision exhausts its
// serializable-retry budget without observing a non-retryable outcome.
var ErrRetryExhausted = errors.New("prnoise: serializable retry exhausted")

// PostgresService is the production Service. It runs the pipeline
// (read-only against the embedder + deduper + identity), then opens a
// serializable Tx and writes the decision row + outbox row atomically.
//
// PR-state mutation (close, label, comment) is NOT performed here; it
// happens in the webhook delivery worker subscribed to
// pr.noise_decision_recorded (ADR-008).
type PostgresService struct {
	conn     PgxConnector
	pipeline *Pipeline
	clock    func() time.Time
}

// NewPostgresService returns a PostgresService backed by conn.
// Callers MUST run the migration that creates collaboration.pr_noise_decisions.
func NewPostgresService(conn PgxConnector, p *Pipeline) (*PostgresService, error) {
	if p == nil {
		return nil, ErrPipelineUnconfigured
	}
	return &PostgresService{
		conn:     conn,
		pipeline: p,
		clock:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// RecordDecision implements Service. Pipeline.Score runs outside the
// Tx (it performs network I/O against the embedder and Qdrant); the Tx
// scope covers only the decision-row upsert and the outbox insert,
// which share fate per ADR-008.
func (s *PostgresService) RecordDecision(ctx context.Context, in PRInput) (Decision, error) {
	d, err := s.pipeline.Score(ctx, in)
	if err != nil {
		return Decision{}, err
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = s.clock()
	}

	if err := s.persistWithRetry(ctx, d); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// persistWithRetry drives the bounded retry loop. Mirrors
// identity.WithSerializableRetry; we keep the loop in-package to avoid
// pulling identity into the prnoise persistence path.
func (s *PostgresService) persistWithRetry(ctx context.Context, d Decision) error {
	delay := 10 * time.Millisecond
	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		err := s.persistOnce(ctx, d)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		if attempt == retryMaxAttempts-1 {
			break
		}
		jitter := time.Duration(rand.Int63n(int64(delay)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay *= 2
		if delay > 100*time.Millisecond {
			delay = 100 * time.Millisecond
		}
	}
	return ErrRetryExhausted
}

// isRetryable mirrors store.IsRetryable but is kept local so the
// package's persistence path doesn't import the data-plane retry
// helper directly. 40001 is the only retryable code we expect.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001"
	}
	return false
}

// persistOnce opens a serializable Tx, upserts the decision row, and
// writes the outbox row. ADR-008 invariant: both rows commit together
// or roll back together. Caller acks on commit, never on Kafka publish.
func (s *PostgresService) persistOnce(ctx context.Context, d Decision) error {
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("prnoise: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := upsertDecisionRow(ctx, tx, d); err != nil {
		return err
	}
	if err := writeNoiseDecisionOutbox(ctx, tx, d); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("prnoise: commit: %w", err)
	}
	committed = true
	return nil
}

// upsertDecisionRow performs INSERT … ON CONFLICT (pr_id) DO UPDATE.
// Re-scoring an existing PR overwrites the prior row; downstream
// consumers must idempotency-key on event_id, not pr_id (ADR-008).
//
// agent_id is stored as NULL when the PR was opened by a human (zero
// UUID sentinel — see PRInput.AgentID docs). duplicate_of likewise.
func upsertDecisionRow(ctx context.Context, tx pgx.Tx, d Decision) error {
	var agentArg any
	if d.AgentID != uuid.Nil {
		agentArg = d.AgentID
	}
	var dupArg any
	if d.DuplicateOf != nil {
		dupArg = *d.DuplicateOf
	}
	const q = `
		INSERT INTO collaboration.pr_noise_decisions
		  (pr_id, repo_id, org_id, agent_id,
		   dedup_score, duplicate_of, quality_score, reputation_score,
		   composite_score, decision_code, reason, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (pr_id) DO UPDATE SET
		  repo_id          = EXCLUDED.repo_id,
		  org_id           = EXCLUDED.org_id,
		  agent_id         = EXCLUDED.agent_id,
		  dedup_score      = EXCLUDED.dedup_score,
		  duplicate_of     = EXCLUDED.duplicate_of,
		  quality_score    = EXCLUDED.quality_score,
		  reputation_score = EXCLUDED.reputation_score,
		  composite_score  = EXCLUDED.composite_score,
		  decision_code    = EXCLUDED.decision_code,
		  reason           = EXCLUDED.reason,
		  decided_at       = EXCLUDED.decided_at
	`
	if _, err := tx.Exec(ctx, q,
		d.PRID, d.RepoID, d.OrgID, agentArg,
		d.DedupScore, dupArg, d.QualityScore, d.ReputationScore,
		d.CompositeScore, string(d.Code), d.Reason, d.DecidedAt,
	); err != nil {
		return fmt.Errorf("prnoise: upsert decision: %w", err)
	}
	return nil
}

// writeNoiseDecisionOutbox writes the pr.noise_decision_recorded row to
// the collaboration outbox. event_id is a UUIDv7 (monotonic per process)
// generated through the data-plane helper so consumers can dedupe on it.
func writeNoiseDecisionOutbox(ctx context.Context, tx pgx.Tx, d Decision) error {
	payload, err := json.Marshal(newNoiseDecisionRecordedPayload(d))
	if err != nil {
		return fmt.Errorf("prnoise: marshal payload: %w", err)
	}
	const q = `
		INSERT INTO collaboration.collaboration_outbox
		  (event_id, aggregate_type, aggregate_id, event_type, payload)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := tx.Exec(ctx, q,
		store.NewEventID(),
		"pr_noise_decision",
		d.PRID,
		EventTypeNoiseDecisionRecorded,
		payload,
	); err != nil {
		return fmt.Errorf("prnoise: write outbox: %w", err)
	}
	return nil
}
