package cirunclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

// CIRunWorkflowName is the registered Temporal workflow type name the
// workflow plane binds a worker to. We carry it as a string literal here
// to keep the application plane free of workflow-definition imports
// (ADR-019).
const CIRunWorkflowName = "CIRunWorkflow"

// CIRunInput is the JSON-serialisable payload handed to the workflow.
// The workflow plane decodes it; we keep the shape minimal so a schema
// change there does not force a breaking change here.
type CIRunInput struct {
	RepoID      uuid.UUID `json:"repo_id"`
	Ref         string    `json:"ref"`
	PrincipalID uuid.UUID `json:"principal_id"`
}

// RunHandle is the typed return of StartCIRun. WorkflowID is the
// Temporal workflow ID; RunID is the specific run instance. Both are
// surfaced to the agent so it can poll the workflow plane for status.
type RunHandle struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id"`
}

// Client is the surface the MCP `ci_trigger` tool calls. Implementations:
//
//   - TemporalClient (production): wraps go.temporal.io/sdk/client.Client
//   - StubClient    (test): returns a fixed RunHandle, records inputs
//   - NilClient     (deployments without Temporal): returns ErrNotConfigured
//     which the MCP layer maps to -32004 not_implemented.
type Client interface {
	StartCIRun(ctx context.Context, in CIRunInput) (RunHandle, error)
}

// ErrNotConfigured is returned by NilClient and surfaces as
// CodeNotImplemented at the MCP layer.
var ErrNotConfigured = errors.New("cirunclient: temporal client not configured")

// TemporalClient is the production wrapper. The TaskQueue is the queue
// the workflow worker listens on (configured by ops; defaults to
// "ci-runs").
type TemporalClient struct {
	c         client.Client
	taskQueue string
}

// NewTemporalClient constructs the production wrapper. taskQueue must be
// non-empty; an empty queue is a configuration bug, not a runtime
// fallback.
func NewTemporalClient(c client.Client, taskQueue string) (*TemporalClient, error) {
	if c == nil {
		return nil, errors.New("cirunclient: nil temporal client")
	}
	if taskQueue == "" {
		return nil, errors.New("cirunclient: empty task queue")
	}
	return &TemporalClient{c: c, taskQueue: taskQueue}, nil
}

// StartCIRun starts the CI workflow and returns the handle. WorkflowID
// is namespaced as "ci-run/<repo_id>/<unix-nanos>" so multiple runs on
// the same repo coexist without collision. We do NOT use a deterministic
// ID — re-running a workflow is the workflow plane's call (signal,
// reset, or fresh ID), not the client's.
func (t *TemporalClient) StartCIRun(ctx context.Context, in CIRunInput) (RunHandle, error) {
	wfID := fmt.Sprintf("ci-run/%s/%d", in.RepoID, ctxNanos(ctx))
	opts := client.StartWorkflowOptions{
		ID:        wfID,
		TaskQueue: t.taskQueue,
	}
	we, err := t.c.ExecuteWorkflow(ctx, opts, CIRunWorkflowName, in)
	if err != nil {
		return RunHandle{}, fmt.Errorf("cirunclient: ExecuteWorkflow: %w", err)
	}
	return RunHandle{WorkflowID: we.GetID(), RunID: we.GetRunID()}, nil
}

// StubClient implements Client for tests. The recorded Last input is
// inspected by tests; Handle is the canned return.
type StubClient struct {
	Handle RunHandle
	Err    error
	Last   CIRunInput
	Called bool
}

func (s *StubClient) StartCIRun(_ context.Context, in CIRunInput) (RunHandle, error) {
	s.Last = in
	s.Called = true
	if s.Err != nil {
		return RunHandle{}, s.Err
	}
	return s.Handle, nil
}

// NilClient is the no-op implementation used in deployments that have
// not wired a Temporal cluster. Always returns ErrNotConfigured.
type NilClient struct{}

func (NilClient) StartCIRun(_ context.Context, _ CIRunInput) (RunHandle, error) {
	return RunHandle{}, ErrNotConfigured
}

// ctxNanos returns wall-clock nanoseconds; carved out as a package
// variable so tests can substitute a deterministic source.
var ctxNanos = func(_ context.Context) int64 { return time.Now().UnixNano() }
