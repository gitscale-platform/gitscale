package rules

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubViolations struct {
	n   int
	err error
}

func (s stubViolations) ViolationCount24h(_ context.Context, _, _ uuid.UUID) (int, error) {
	return s.n, s.err
}

func TestAgentsMDViolations(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		c         AgentsMDViolationCounter
		wantFires bool
	}{
		{"nil", nil, false},
		{"none", stubViolations{n: 0}, false},
		{"below_threshold", stubViolations{n: AgentsMDViolationThreshold - 1}, false},
		{"at_threshold", stubViolations{n: AgentsMDViolationThreshold}, true},
		{"above", stubViolations{n: 99}, true},
		{"error_fail_soft", stubViolations{err: errors.New("agentsmd down")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := AgentsMDViolations(tc.c)
			r, err := rule(ctx, Input{ReporterID: uuid.New(), RepoID: uuid.New()})
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
