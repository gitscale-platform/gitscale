package rules

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubCounter struct {
	n   int
	err error
}

func (s stubCounter) Count(_ context.Context, _ uuid.UUID) (int, error) {
	return s.n, s.err
}

func TestReporterRate(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		c         ReporterRateCounter
		wantFires bool
	}{
		{"nil_counter", nil, false},
		{"below_threshold", stubCounter{n: 5}, false},
		{"at_threshold", stubCounter{n: ReporterRateThreshold}, true},
		{"way_above", stubCounter{n: 999}, true},
		{"counter_error_soft_skips", stubCounter{err: errors.New("redis down")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := ReporterRate(tc.c)
			r, err := rule(ctx, Input{ReporterID: uuid.New()})
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			fired := r.Signal.Weight > 0
			if fired != tc.wantFires {
				t.Fatalf("fired=%v want %v", fired, tc.wantFires)
			}
		})
	}
}
