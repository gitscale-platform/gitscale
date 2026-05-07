package workflow

// Bundle is the unit of registration for one task queue. A worker registers
// exactly one Bundle per queue it serves. Domain packages export a Bundle()
// function that the worker entrypoint (cmd/workflow-worker) collects without
// modification — adding a new workflow requires no change to main.go.
//
// The slice element type is any so this package compiles without depending
// on the Temporal SDK; the worker entrypoint passes them through to
// worker.Worker.RegisterWorkflow / RegisterActivity which accept any.
type Bundle struct {
	// TaskQueue must match one of the constants in queues.go.
	TaskQueue string
	// Workflows are workflow funcs (or struct-method pairs).
	Workflows []any
	// Activities are activity funcs (or struct-method pairs).
	Activities []any
}

// Registrar is the minimal Temporal-worker interface the Bundle.Apply method
// needs. *worker.Worker satisfies it; tests can inject a fake to assert
// registration order without spinning a real worker.
type Registrar interface {
	RegisterWorkflow(any)
	RegisterActivity(any)
}

// Apply registers all workflows and activities in this Bundle on r in the
// order they appear in the slices. Order matters only when the same name is
// registered twice (later wins per Temporal SDK semantics) — Bundles should
// not register the same identifier twice.
func (b Bundle) Apply(r Registrar) {
	for _, wf := range b.Workflows {
		r.RegisterWorkflow(wf)
	}
	for _, a := range b.Activities {
		r.RegisterActivity(a)
	}
}
