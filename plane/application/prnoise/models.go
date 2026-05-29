package prnoise

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DecisionCode is the closed enum of terminal pipeline outcomes.
// Downstream consumers (webhooks, audit, billing) treat this as a stable
// contract; new values require an ADR amendment.
type DecisionCode string

const (
	// DecisionAutoMergeEligible labels the PR for automated merge.
	DecisionAutoMergeEligible DecisionCode = "auto_merge_eligible"
	// DecisionMaintainerReview routes the PR to the human review queue.
	DecisionMaintainerReview DecisionCode = "maintainer_review"
	// DecisionReject closes the PR with a reason. Used for both duplicate
	// PRs and PRs whose composite falls below the reject band.
	DecisionReject DecisionCode = "reject"
)

// Valid reports whether c is one of the three known DecisionCode values.
func (c DecisionCode) Valid() bool {
	switch c {
	case DecisionAutoMergeEligible, DecisionMaintainerReview, DecisionReject:
		return true
	}
	return false
}

// PRInput is the tuple consumed by Pipeline.Score. The pipeline never
// mutates the input.
type PRInput struct {
	PRID         uuid.UUID
	RepoID       uuid.UUID
	OrgID        uuid.UUID
	AgentID      uuid.UUID // zero UUID indicates a human author
	Title        string
	Description  string
	DiffStats    DiffStats
	CIResult     CIResult
	AgentsMDOK   bool
	OpenedAt     time.Time
}

// DiffStats is the diff projection consumed by the rule-based quality
// scorer. The pipeline does not need the diff body itself.
type DiffStats struct {
	Additions    int
	Deletions    int
	FilesChanged int
	FilePaths    []string
	ChurnSamples []ChurnSample
}

// ChurnSample is one (file, touches_30d) pair.
type ChurnSample struct {
	FilePath  string
	Touches30 int
}

// CIResult projects the CI run result needed for the quality signals.
type CIResult struct {
	Build          bool
	Test           bool
	LintViolations int
	CoverageDelta  float64
}

// Decision is the persisted output of one pipeline run. All score fields
// live in [0, 1]; the database CHECK constraints mirror that range.
type Decision struct {
	PRID            uuid.UUID
	RepoID          uuid.UUID
	OrgID           uuid.UUID
	AgentID         uuid.UUID
	DedupScore      float64
	DuplicateOf     *uuid.UUID
	QualityScore    float64
	ReputationScore float64
	CompositeScore  float64
	Code            DecisionCode
	Reason          string
	DecidedAt       time.Time
}

// Embedder turns text into a normalised dense vector. Defined in the
// prnoise root package so the sub-packages provide implementations
// without importing each other (avoiding import cycles).
//
// Production wires the same embedding service Phase 1 used; the package
// owns no embedding model (orthogonal infrastructure under ADR-016).
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// DuplicateHit is the projection returned by Deduper.NearestDuplicate
// when the top-1 cosine meets the dedup threshold from ADR-016.
type DuplicateHit struct {
	PRID       uuid.UUID
	Similarity float64
}

// Deduper is the application-plane interface over the Qdrant ANN store
// (ADR-021: Qdrant scoped to PR dedup only). repoID is the per-repo
// dedup scope; implementations gate on the prnoise.cross_org_dedup
// feature flag for whether to honour it.
type Deduper interface {
	NearestDuplicate(ctx context.Context, repoID uuid.UUID, vec []float32) (*DuplicateHit, error)
}

// QualitySignalScorer is the Stage 2 swap surface (ADR-017). The
// shipping implementation is rule-based; an ML quality scorer can land
// without touching the pipeline.
type QualitySignalScorer interface {
	Score(ctx context.Context, in PRInput) float64
}

// ReputationScorer is the Stage 3 swap surface (ADR-017) — the swap
// surface CLAUDE.md's open question (July 2026, rule-based vs. ML
// reputation) hinges on. Implementations MUST handle uuid.Nil as the
// "human author" sentinel.
type ReputationScorer interface {
	Score(ctx context.Context, agentID uuid.UUID) (float64, error)
}
