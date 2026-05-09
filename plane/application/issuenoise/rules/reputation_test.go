package rules

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubRep struct {
	score float64
	err   error
}

func (s stubRep) Score(_ context.Context, _ uuid.UUID) (float64, error) {
	return s.score, s.err
}

func TestReputation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		score    float64
		err      error
		wantFire bool
		wantCat  Category
	}{
		{"high_no_fire", 0.95, nil, false, CategorySpam},
		{"default_no_fire", 0.5, nil, false, CategorySpam},
		{"low_quality_band", 0.25, nil, true, CategoryLowQuality},
		{"spam_band", 0.05, nil, true, CategorySpam},
		{"at_low_quality_floor_no_fire", ReputationLowQualityFloor, nil, false, CategorySpam},
		{"error_fail_soft", 0.0, errors.New("identity timeout"), false, CategorySpam},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := Reputation(stubRep{score: tc.score, err: tc.err})
			r, err := rule(ctx, Input{ReporterID: uuid.New()})
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			fired := r.Signal.Weight > 0
			if fired != tc.wantFire {
				t.Fatalf("fired=%v want %v signal=%+v", fired, tc.wantFire, r.Signal)
			}
			if fired && r.Category != tc.wantCat {
				t.Errorf("category=%v want %v", r.Category, tc.wantCat)
			}
		})
	}

	t.Run("nil_lookup", func(t *testing.T) {
		rule := Reputation(nil)
		r, err := rule(ctx, Input{ReporterID: uuid.New()})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if r.Signal.Weight != 0 {
			t.Fatalf("expected no fire on nil lookup")
		}
	})
}
