package ci

import (
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
)

// Bundle returns the workflow-only registration set for the CI pipelines
// queue. The CIJobWorkflow is registered here; activities are registered
// by plane/workflow/runner.Bundle on the same task queue. The worker
// entrypoint composes both bundles into one Worker.
func Bundle() gswf.Bundle {
	return gswf.Bundle{
		TaskQueue: gswf.QueueCIPipelines,
		Workflows: []any{CIJobWorkflow},
	}
}
