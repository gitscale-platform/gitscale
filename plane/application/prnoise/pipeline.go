package prnoise

import (
	"context"
	"fmt"
)

// Pipeline runs the four stages of the PR noise pipeline in order:
// dedup → quality → reputation → composite routing.
//
// Stateless past construction; Score is safe for concurrent use as long
// as the injected dependencies are too. Stage failures bubble up as
// wrapped errors; the pipeline never returns a partial Decision.
type Pipeline struct {
	Embedder   Embedder
	Deduper    Deduper
	Quality    QualitySignalScorer
	Reputation ReputationScorer
	Router     *CompositeRouter
}

// NewPipeline returns a fully-wired Pipeline. The router is constructed
// from cfg so callers don't have to plumb three values; QualitySignalScorer
// and ReputationScorer remain injected (ADR-017 swap surfaces).
func NewPipeline(
	emb Embedder,
	dd Deduper,
	q QualitySignalScorer,
	rep ReputationScorer,
	cfg Config,
) *Pipeline {
	return &Pipeline{
		Embedder:   emb,
		Deduper:    dd,
		Quality:    q,
		Reputation: rep,
		Router:     NewCompositeRouter(cfg.Composite, cfg.Bands),
	}
}

// embedText is the input to Embedder. Title + body matches the Phase 1
// shadow run; title alone would lose too much signal on stub PRs.
func embedText(in PRInput) string {
	return in.Title + "\n\n" + in.Description
}

// Score runs all four stages and returns a fully-populated Decision.
// The Decision is NOT persisted; callers (Service implementations) are
// responsible for writing it inside a Tx alongside the outbox row
// (ADR-008).
func (p *Pipeline) Score(ctx context.Context, in PRInput) (Decision, error) {
	// Stage 1: semantic dedup.
	vec, err := p.Embedder.Embed(ctx, embedText(in))
	if err != nil {
		return Decision{}, fmt.Errorf("prnoise: embed: %w", err)
	}
	hit, err := p.Deduper.NearestDuplicate(ctx, in.RepoID, vec)
	if err != nil {
		return Decision{}, fmt.Errorf("prnoise: dedup: %w", err)
	}

	// Stage 2: quality signals.
	q := p.Quality.Score(ctx, in)

	// Stage 3: reputation.
	rep, err := p.Reputation.Score(ctx, in.AgentID)
	if err != nil {
		return Decision{}, fmt.Errorf("prnoise: reputation: %w", err)
	}

	// Stage 4: composite router.
	composite, code, reason := p.Router.Decide(q, rep, hit)

	d := Decision{
		PRID:            in.PRID,
		RepoID:          in.RepoID,
		OrgID:           in.OrgID,
		AgentID:         in.AgentID,
		QualityScore:    q,
		ReputationScore: rep,
		CompositeScore:  composite,
		Code:            code,
		Reason:          reason,
		DecidedAt:       in.OpenedAt,
	}
	if hit != nil {
		d.DedupScore = 1.0
		dup := hit.PRID
		d.DuplicateOf = &dup
	}
	return d, nil
}
