package billing

import "go.temporal.io/sdk/activity"

// registerActivityOpts produces consistent registration options used by the
// workflow tests. Pulled into a helper because the testsuite re-registers on
// every NewTestWorkflowEnvironment.
func registerActivityOpts() activity.RegisterOptions {
	return activity.RegisterOptions{Name: ActivityNameCreatePartition}
}
