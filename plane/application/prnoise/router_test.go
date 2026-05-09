package prnoise

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

const routerEpsilon = 1e-9

func nearR(a, b float64) bool { return math.Abs(a-b) < routerEpsilon }

func defaultRouter() *CompositeRouter {
	cfg := DefaultConfig()
	return NewCompositeRouter(cfg.Composite, cfg.Bands)
}

func TestRouterDecide_DuplicateAlwaysRejects(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	hit := &DuplicateHit{PRID: uuid.New(), Similarity: 0.99}
	composite, code, reason := r.Decide(1.0, 1.0, hit)
	if code != DecisionReject {
		t.Errorf("code = %v, want reject", code)
	}
	if reason != ReasonDuplicate {
		t.Errorf("reason = %q, want %q", reason, ReasonDuplicate)
	}
	if !nearR(composite, 0.0) {
		t.Errorf("composite = %v, want 0.0", composite)
	}
}

func TestRouterDecide_AutoMergeBoundary(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	composite, code, reason := r.Decide(1.0, 1.0, nil)
	if code != DecisionAutoMergeEligible {
		t.Errorf("code = %v, want auto_merge_eligible", code)
	}
	if reason != ReasonCompositeAboveAutoMerge {
		t.Errorf("reason = %q, want %q", reason, ReasonCompositeAboveAutoMerge)
	}
	if !nearR(composite, 0.9) {
		t.Errorf("composite = %v, want 0.9", composite)
	}
}

func TestRouterDecide_MaintainerReviewBand(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	composite, code, reason := r.Decide(0.5, 0.5, nil)
	if code != DecisionMaintainerReview {
		t.Errorf("code = %v, want maintainer_review", code)
	}
	if reason != ReasonCompositeInReviewBand {
		t.Errorf("reason = %q, want %q", reason, ReasonCompositeInReviewBand)
	}
	if !nearR(composite, 0.45) {
		t.Errorf("composite = %v, want 0.45", composite)
	}
}

func TestRouterDecide_RejectBelowBand(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	composite, code, reason := r.Decide(0.1, 0.1, nil)
	if code != DecisionReject {
		t.Errorf("code = %v, want reject", code)
	}
	if reason != ReasonCompositeBelowReject {
		t.Errorf("reason = %q, want %q", reason, ReasonCompositeBelowReject)
	}
	if !nearR(composite, 0.09) {
		t.Errorf("composite = %v, want 0.09", composite)
	}
}

func TestRouterDecide_AtAutoMergeBandIsAutoMerge(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	composite, code, _ := r.Decide(1.0, 0.875, nil)
	if code != DecisionAutoMergeEligible {
		t.Errorf("at AutoMerge: code = %v, want auto_merge_eligible", code)
	}
	if !nearR(composite, 0.85) {
		t.Errorf("composite = %v, want 0.85", composite)
	}
}

func TestRouterDecide_AtRejectBandIsMaintainerReview(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	composite, code, _ := r.Decide(0.6, 0.0, nil)
	if code != DecisionMaintainerReview {
		t.Errorf("at Reject: code = %v, want maintainer_review", code)
	}
	if !nearR(composite, 0.30) {
		t.Errorf("composite = %v, want 0.30", composite)
	}
}

func TestRouterDecide_AllDecisionCodesReachable(t *testing.T) {
	t.Parallel()
	r := defaultRouter()
	seen := map[DecisionCode]bool{}
	_, c, _ := r.Decide(1.0, 1.0, nil)
	seen[c] = true
	_, c, _ = r.Decide(0.5, 0.5, nil)
	seen[c] = true
	_, c, _ = r.Decide(0.0, 0.0, nil)
	seen[c] = true
	_, c, _ = r.Decide(1.0, 1.0, &DuplicateHit{Similarity: 1.0})
	seen[c] = true
	for _, code := range []DecisionCode{
		DecisionAutoMergeEligible,
		DecisionMaintainerReview,
		DecisionReject,
	} {
		if !seen[code] {
			t.Errorf("DecisionCode %q not reachable from any input", code)
		}
	}
}
