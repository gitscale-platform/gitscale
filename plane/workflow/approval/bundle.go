package approval

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the activity-only registration set for the approval
// activity. The bundle has no workflows: ApprovalActivity is invoked from
// other workflows (CI, agent-session, etc.) on their own task queues. We
// register on QueueAgentSessions because agent plans are the dominant
// expected source; CI and billing workflows that need it can register the
// same activity on their queues by composing this bundle.
func Bundle(act *Activity) gswf.Bundle {
	return gswf.Bundle{
		TaskQueue: gswf.QueueAgentSessions,
		Workflows: nil,
		Activities: []any{
			gswf.NamedActivity{Name: ActivityNameRequestApproval, Activity: act.Execute},
		},
	}
}
