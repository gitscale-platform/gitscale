package issuenoise

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/issuenoise/rules"
	"github.com/google/uuid"
)

// fakeRule produces a fixed Result.
func fakeRule(cat rules.Category, name string, weight float64) rules.Rule {
	return func(_ context.Context, _ rules.Input) (rules.Result, error) {
		return rules.Result{
			Category: cat,
			Signal:   rules.Signal{Name: name, Weight: weight, Detail: "test"},
		}, nil
	}
}

func TestRuleScorer_AggregatesByCategory(t *testing.T) {
	s := NewRuleScorerWithRules([]rules.Rule{
		fakeRule(rules.CategorySpam, "a", 0.3),
		fakeRule(rules.CategorySpam, "b", 0.2),
		fakeRule(rules.CategoryLowQuality, "c", 0.5),
	})
	got, err := s.Score(context.Background(), IssueDraft{ID: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.Spam != 0.5 {
		t.Errorf("spam=%v want 0.5", got.Spam)
	}
	if got.LowQuality != 0.5 {
		t.Errorf("low_quality=%v want 0.5", got.LowQuality)
	}
	if got.Duplicate != 0 {
		t.Errorf("duplicate=%v want 0", got.Duplicate)
	}
	if len(got.Signals) != 3 {
		t.Errorf("signals=%d want 3", len(got.Signals))
	}
	if got.ScorerVersion != RuleScorerVersion {
		t.Errorf("version=%q", got.ScorerVersion)
	}
}

func TestRuleScorer_ClampsAtOne(t *testing.T) {
	s := NewRuleScorerWithRules([]rules.Rule{
		fakeRule(rules.CategorySpam, "a", 0.7),
		fakeRule(rules.CategorySpam, "b", 0.7),
	})
	got, _ := s.Score(context.Background(), IssueDraft{})
	if got.Spam != 1.0 {
		t.Fatalf("expected clamp to 1.0, got %v", got.Spam)
	}
}

func TestRuleScorer_PropagatesDuplicateOf(t *testing.T) {
	parent := uuid.New()
	dupRule := func(_ context.Context, _ rules.Input) (rules.Result, error) {
		p := parent
		return rules.Result{
			Category:    rules.CategoryDuplicate,
			DuplicateOf: &p,
			Signal:      rules.Signal{Name: "dup", Weight: 0.92, Detail: "x"},
		}, nil
	}
	s := NewRuleScorerWithRules([]rules.Rule{dupRule})
	got, err := s.Score(context.Background(), IssueDraft{})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.DuplicateOf == nil || *got.DuplicateOf != parent {
		t.Fatalf("DuplicateOf=%v want %v", got.DuplicateOf, parent)
	}
}

func TestRuleScorer_RuleErrorAggregated(t *testing.T) {
	errRule := func(_ context.Context, _ rules.Input) (rules.Result, error) {
		return rules.Result{}, errors.New("boom")
	}
	good := fakeRule(rules.CategorySpam, "ok", 0.4)
	s := NewRuleScorerWithRules([]rules.Rule{errRule, good})
	got, err := s.Score(context.Background(), IssueDraft{})
	if err == nil {
		t.Fatalf("expected aggregated error")
	}
	if got.Spam != 0.4 {
		t.Errorf("spam=%v want 0.4 (good rule still recorded)", got.Spam)
	}
}

func TestRuleScorer_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewRuleScorerWithRules([]rules.Rule{fakeRule(rules.CategorySpam, "x", 0.5)})
	if _, err := s.Score(ctx, IssueDraft{}); err == nil {
		t.Fatalf("expected context.Canceled to propagate")
	}
}

func TestRuleScorer_DefaultRegistryAllNilDeps(t *testing.T) {
	// Smoke: with all nil deps, the scorer should still run and produce
	// only the deps-free rules' contributions (link_density, length,
	// language). For a normal-looking body none of those should fire.
	s := NewRuleScorer(nil, nil, nil, nil)
	got, err := s.Score(context.Background(), IssueDraft{
		Body: strings.Repeat("ok bug reproduction steps here ", 5),
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.Spam != 0 || got.LowQuality != 0 || got.Duplicate != 0 {
		t.Fatalf("expected zero scores, got %+v", got)
	}
}
