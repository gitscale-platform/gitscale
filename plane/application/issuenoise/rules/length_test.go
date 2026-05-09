package rules

import (
	"context"
	"strings"
	"testing"
)

func TestLength(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		body      string
		wantFires bool
	}{
		{"too_short", "tiny", true},
		{"empty", "", true},
		{"just_below_floor", strings.Repeat("a", LengthFloor-1), true},
		{"at_floor", strings.Repeat("a", LengthFloor), false},
		{"reasonable", strings.Repeat("a", 500), false},
		{"at_ceiling", strings.Repeat("a", LengthCeiling), false},
		{"above_ceiling", strings.Repeat("a", LengthCeiling+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Length(ctx, Input{Body: tc.body})
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
