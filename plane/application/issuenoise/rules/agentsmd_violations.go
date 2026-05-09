package rules

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AgentsMDViolationThreshold is the per-(agent, repo, 24h) violation
// count above which the rule fires. ≥ 3 violations is "this agent is
// repeatedly ignoring the AGENTS.md policy" — strong spam signal.
const AgentsMDViolationThreshold = 3

// AgentsMDViolationWeight is the spam contribution when fired.
const AgentsMDViolationWeight = 0.40

// AgentsMDViolationCounter is the rule's dependency. A real impl wraps
// the agentsmd plane's ViolationCount(ctx, agentID, repoID, since)
// surface; tests inject a stub. Cached in Redis under a 1-hour bucket
// in production to keep p99 acceptable.
type AgentsMDViolationCounter interface {
	ViolationCount24h(ctx context.Context, agentID, repoID uuid.UUID) (int, error)
}

// AgentsMDViolations returns a rule closure that consults c. Failing
// the lookup contributes nothing (fail-soft).
func AgentsMDViolations(c AgentsMDViolationCounter) Rule {
	return func(ctx context.Context, in Input) (Result, error) {
		if c == nil {
			return Result{}, nil
		}
		n, err := c.ViolationCount24h(ctx, in.ReporterID, in.RepoID)
		if err != nil {
			return Result{}, nil
		}
		if n < AgentsMDViolationThreshold {
			return Result{}, nil
		}
		return Result{
			Category: CategorySpam,
			Signal: Signal{
				Name:   "agentsmd_violations",
				Weight: AgentsMDViolationWeight,
				Detail: fmt.Sprintf("violations_24h=%d (threshold=%d)", n, AgentsMDViolationThreshold),
			},
		}, nil
	}
}
