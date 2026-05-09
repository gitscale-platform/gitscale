package runner

import (
	"context"
	"errors"
	"fmt"
)

// ActivityNameRunJob is the registered activity name.
const ActivityNameRunJob = "runner.RunJob"

// RunJobActivity issues a command set over vsock to the in-VM agent and
// streams stdout/stderr to the configured log sink. The workflow
// invokes this with a single-attempt retry policy — CI jobs are NOT
// idempotent (they have side effects: pushes, builds, deploys), so a
// retry would silently double-execute the user's command.
type RunJobActivity struct {
	provisioner MicroVMProvisioner
}

// NewRunJobActivity wraps the provisioner.
func NewRunJobActivity(p MicroVMProvisioner) (*RunJobActivity, error) {
	if p == nil {
		return nil, errors.New("runner.NewRunJobActivity: provisioner is nil")
	}
	return &RunJobActivity{provisioner: p}, nil
}

// Execute runs the command set and returns the consolidated JobResult.
func (a *RunJobActivity) Execute(ctx context.Context, in RunInput) (JobResult, error) {
	r, err := a.provisioner.Run(ctx, in)
	if err != nil {
		return JobResult{}, fmt.Errorf("runner.RunJob: %w", err)
	}
	return r, nil
}
