package canary

import (
	"time"

	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"go.temporal.io/sdk/workflow"
)

// CanaryWorkflow runs the HealthActivity once with the project's default
// retry policy and returns its result. Used by the canary integration test
// to prove worker boot and activity dispatch end-to-end.
//
// Determinism: no time.Now, no rand, no goroutines, no maps with workflow
// calls — the determinism lint guards this file.
func CanaryWorkflow(ctx workflow.Context) (string, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         gswf.DefaultRetryPolicy(),
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// Dispatch by registered name so the workflow does not need a reference
	// to the activity instance (which lives in the worker process and varies
	// between tests and production).
	var result string
	if err := workflow.ExecuteActivity(ctx, HealthActivityName).Get(ctx, &result); err != nil {
		return "", err
	}
	return result, nil
}

// HealthActivityName is the registered name of the canary's health-check
// activity. Bundle and tests use the same constant so dispatch resolves
// regardless of which HealthActivity instance is wired in.
const HealthActivityName = "canary.health"
