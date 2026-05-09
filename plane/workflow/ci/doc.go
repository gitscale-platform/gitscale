// Package ci defines CIJobWorkflow — the single Temporal workflow for
// running one CI job to completion on a Firecracker microVM (#110,
// ADR-002, ADR-003). The workflow body is deterministic: no time.*,
// no os.*, no net/*, no math/rand, no goroutines, no channels. All side
// effects live in the activities defined in plane/workflow/runner.
//
// Routing: assignTier is a pure function of (PrincipalKind, Annotations).
// Agent traffic without the require-hot-pool annotation defaults to the
// cold pool — the architecture's agent-default rule (architecture.md
// §2.4 / §6). Humans default to hot.
//
// Saga: teardown is the only compensation. The workflow registers it via
// workflow.NewDisconnectedContext + defer-style invocation so the VM is
// reaped even when the workflow is cancelled or the run activity fails.
// Teardown is idempotent (runner.TeardownVMActivity).
//
// Plane boundary (ADR-019):
//
//   - This package may import plane/workflow/runner and the Temporal SDK.
//   - It MUST NOT import plane/data/store, plane/data/cache, or any
//     plane/application/* package.
//   - Architecture lint blocks Docker, gVisor, runc, runsc, podman, and
//     any container-runtime client at the runner-package boundary.
package ci
