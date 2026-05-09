package runner

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Deps bundles every runner-level activity dep. Constructed in the worker
// entrypoint (cmd/workflow-worker) and passed to Bundle. Pass nil to skip
// CI runner registration when the worker is provisioned without the host
// pool deps (dev mode).
type Deps struct {
	BootCold *BootColdVMActivity
	LeaseHot *LeaseHotVMActivity
	RunJob   *RunJobActivity
	Teardown *TeardownVMActivity
	Emit     *EmitUsageEventActivity
}

// Bundle returns the activity-only registration set for the CI pipelines
// queue. The CIJobWorkflow is registered separately by plane/workflow/ci
// to keep the workflow definition close to its tests.
//
// All five activities register with explicit names so workflow tests can
// dispatch by string without holding activity references.
func Bundle(deps *Deps) gswf.Bundle {
	if deps == nil {
		return gswf.Bundle{TaskQueue: gswf.QueueCIPipelines}
	}
	return gswf.Bundle{
		TaskQueue: gswf.QueueCIPipelines,
		Activities: []any{
			gswf.NamedActivity{Name: ActivityNameBootColdVM, Activity: deps.BootCold.Execute},
			gswf.NamedActivity{Name: ActivityNameLeaseHotVM, Activity: deps.LeaseHot.Execute},
			gswf.NamedActivity{Name: ActivityNameRunJob, Activity: deps.RunJob.Execute},
			gswf.NamedActivity{Name: ActivityNameTeardownVM, Activity: deps.Teardown.Execute},
			gswf.NamedActivity{Name: ActivityNameEmitUsageEvent, Activity: deps.Emit.Execute},
		},
	}
}
