package runner

import (
	"context"
	"errors"
	"fmt"
)

// ActivityNameTeardownVM is the registered activity name.
const ActivityNameTeardownVM = "runner.TeardownVM"

// TeardownVMActivity is idempotent on MicroVMHandle.ID. ErrAlreadyTorndown
// and ErrNotFound are swallowed as success — Temporal will retry tears,
// and double-teardown must be safe. Any other error is surfaced for the
// retry policy to handle.
type TeardownVMActivity struct {
	provisioner MicroVMProvisioner
}

// NewTeardownVMActivity wraps the provisioner.
func NewTeardownVMActivity(p MicroVMProvisioner) (*TeardownVMActivity, error) {
	if p == nil {
		return nil, errors.New("runner.NewTeardownVMActivity: provisioner is nil")
	}
	return &TeardownVMActivity{provisioner: p}, nil
}

// Execute calls Teardown on the provisioner. ErrAlreadyTorndown and
// ErrNotFound are translated to success (idempotency contract per spec
// §"Idempotency key for teardown").
func (a *TeardownVMActivity) Execute(ctx context.Context, vmID string) error {
	if vmID == "" {
		// Empty handle — workflow boot path failed before producing a
		// vmID. Treat as success: nothing to tear down.
		return nil
	}
	err := a.provisioner.Teardown(ctx, vmID)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAlreadyTorndown) || errors.Is(err, ErrNotFound) {
		return nil
	}
	return fmt.Errorf("runner.TeardownVM: %w", err)
}
