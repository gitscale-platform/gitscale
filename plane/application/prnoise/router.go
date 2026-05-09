package prnoise

// Stable Reason strings emitted by CompositeRouter.Decide. Downstream
// consumers (rejection-comment template, audit log) match on these
// exactly; new values require a coordinated PR with the consumer.
const (
	ReasonDuplicate              = "duplicate"
	ReasonCompositeBelowReject   = "composite_below_reject"
	ReasonCompositeInReviewBand  = "composite_in_review_band"
	ReasonCompositeAboveAutoMerge = "composite_above_auto_merge"
)

// CompositeRouter blends quality and reputation into a composite score
// and routes the result to one of three terminal DecisionCodes — Stage
// 4 of the PR noise pipeline.
//
// Algorithm (spec §Stage 4):
//
//	dedupScore = 1.0 if dup != nil else 0.0
//	base       = Quality*qualityScore + Reputation*reputationScore
//	composite  = base * (1 - dedupScore * DedupPenalty)
//	if dup != nil          → reject (Reason="duplicate")
//	else if composite ≥ AutoMerge → auto_merge_eligible
//	else if composite ≥ Reject    → maintainer_review
//	else                          → reject
type CompositeRouter struct {
	Weights CompositeWeights
	Bands   RouterBands
}

// NewCompositeRouter returns a router with the given weights and bands.
func NewCompositeRouter(w CompositeWeights, b RouterBands) *CompositeRouter {
	return &CompositeRouter{Weights: w, Bands: b}
}

// Decide computes composite, code, and reason from the three stage
// outputs. dup == nil means no semantic duplicate was found.
func (r *CompositeRouter) Decide(qualityScore, reputationScore float64, dup *DuplicateHit) (composite float64, code DecisionCode, reason string) {
	dedupScore := 0.0
	if dup != nil {
		dedupScore = 1.0
	}
	base := r.Weights.Quality*qualityScore + r.Weights.Reputation*reputationScore
	composite = base * (1 - dedupScore*r.Weights.DedupPenalty)
	if composite < 0 {
		composite = 0
	}
	if composite > 1 {
		composite = 1
	}

	switch {
	case dup != nil:
		return composite, DecisionReject, ReasonDuplicate
	case composite >= r.Bands.AutoMerge:
		return composite, DecisionAutoMergeEligible, ReasonCompositeAboveAutoMerge
	case composite >= r.Bands.Reject:
		return composite, DecisionMaintainerReview, ReasonCompositeInReviewBand
	default:
		return composite, DecisionReject, ReasonCompositeBelowReject
	}
}
