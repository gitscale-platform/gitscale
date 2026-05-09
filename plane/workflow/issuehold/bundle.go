package issuehold

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the registration set for the issue-hold expiry
// workflow on the QueueAgentSessions task queue. The activity is
// registered under ActivityNameAutoCloseIfStillHeld so the workflow
// can dispatch by stable string name across replays.
func Bundle(act *AutoCloseActivity) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue: gswf.QueueAgentSessions,
		Workflows: []any{IssueHoldExpiryWorkflow},
		Activities: []any{
			gswf.NamedActivity{Name: ActivityNameAutoCloseIfStillHeld, Activity: act.Execute},
		},
	}
}
