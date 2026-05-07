package canary

import (
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the canary's registration unit for the QueueBillingMaintenance
// task queue (chosen because it is the only active queue at #33-B launch).
// The worker entrypoint imports canary and calls Bundle(deps).Apply(worker).
//
// The activity is registered under HealthActivityName so the workflow can
// dispatch it by name without holding a reference to the activity struct.
// The Bundle.Activities slice carries a NamedActivity wrapper; the
// Registrar adapter in cmd/workflow-worker recognises the wrapper and uses
// RegisterActivityWithOptions.
func Bundle(c cache.CacheStore) gswf.Bundle {
	a := NewHealthActivity(c)
	return gswf.Bundle{
		TaskQueue:  gswf.QueueBillingMaintenance,
		Workflows:  []any{CanaryWorkflow},
		Activities: []any{gswf.NamedActivity{Name: HealthActivityName, Activity: a.Run}},
	}
}
