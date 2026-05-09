// Package runnertest exposes an in-memory MicroVMProvisioner used by
// every runner / ci unit and workflow test. The real Firecracker
// integration is build-tagged and operator-run; this fake is the default
// for `go test ./...`.
package runnertest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gitscale-platform/gitscale/plane/workflow/runner"
)

// Fake records every provisioner call and returns scripted handles. The
// zero value is usable; configure scripted error / handle responses with
// the SetXxx methods. Safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	// scripted overrides — when nil, the corresponding method returns a
	// deterministic synthetic handle / result.
	bootCold func(in runner.BootInput) (runner.MicroVMHandle, error)
	leaseHot func(in runner.LeaseInput) (runner.MicroVMHandle, error)
	run      func(in runner.RunInput) (runner.JobResult, error)
	teardown func(vmID string) error

	// recorded calls — append-only audit trail asserted by tests.
	bootCalls     []runner.BootInput
	leaseCalls    []runner.LeaseInput
	runCalls      []runner.RunInput
	teardownCalls []string

	// liveVMs lets the default Teardown impl return ErrNotFound for
	// unknown IDs and ErrAlreadyTorndown on the second call.
	liveVMs map[string]bool
}

// NewFake returns a ready-to-use fake.
func NewFake() *Fake {
	return &Fake{liveVMs: make(map[string]bool)}
}

// SetBootCold installs a scripted BootCold implementation.
func (f *Fake) SetBootCold(fn func(runner.BootInput) (runner.MicroVMHandle, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bootCold = fn
}

// SetLeaseHot installs a scripted LeaseHot implementation.
func (f *Fake) SetLeaseHot(fn func(runner.LeaseInput) (runner.MicroVMHandle, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseHot = fn
}

// SetRun installs a scripted Run implementation.
func (f *Fake) SetRun(fn func(runner.RunInput) (runner.JobResult, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = fn
}

// SetTeardown installs a scripted Teardown implementation.
func (f *Fake) SetTeardown(fn func(vmID string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardown = fn
}

// BootCold implements MicroVMProvisioner.
func (f *Fake) BootCold(_ context.Context, in runner.BootInput) (runner.MicroVMHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bootCalls = append(f.bootCalls, in)
	if f.bootCold != nil {
		h, err := f.bootCold(in)
		if err == nil {
			f.liveVMs[h.ID] = true
		}
		return h, err
	}
	h := runner.MicroVMHandle{
		ID:             fmt.Sprintf("vm-cold-%s", in.JobID),
		VsockCID:       3,
		IPv4:           "10.0.0.2",
		KernelImage:    "vmlinuz-cold",
		RootfsSnapshot: "rootfs-cold.snap",
	}
	f.liveVMs[h.ID] = true
	return h, nil
}

// LeaseHot implements MicroVMProvisioner.
func (f *Fake) LeaseHot(_ context.Context, in runner.LeaseInput) (runner.MicroVMHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseCalls = append(f.leaseCalls, in)
	if f.leaseHot != nil {
		h, err := f.leaseHot(in)
		if err == nil {
			f.liveVMs[h.ID] = true
		}
		return h, err
	}
	h := runner.MicroVMHandle{
		ID:             fmt.Sprintf("vm-hot-%s", in.JobID),
		VsockCID:       4,
		IPv4:           "10.0.0.3",
		KernelImage:    "vmlinuz-hot",
		RootfsSnapshot: "rootfs-hot.snap",
	}
	f.liveVMs[h.ID] = true
	return h, nil
}

// Run implements MicroVMProvisioner.
func (f *Fake) Run(_ context.Context, in runner.RunInput) (runner.JobResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCalls = append(f.runCalls, in)
	if f.run != nil {
		return f.run(in)
	}
	return runner.JobResult{
		ExitCode:       0,
		DurationMS:     123,
		BytesIngressed: 1024,
		BytesEgressed:  2048,
		PeakMemoryKB:   65536,
		LogsObjectURI:  "s3://test-logs/" + in.VMID,
	}, nil
}

// Teardown implements MicroVMProvisioner. Default behaviour: first call
// removes the VM from the live set and returns nil; second call returns
// ErrAlreadyTorndown; unknown id returns ErrNotFound.
func (f *Fake) Teardown(_ context.Context, vmID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardownCalls = append(f.teardownCalls, vmID)
	if f.teardown != nil {
		err := f.teardown(vmID)
		if err == nil {
			delete(f.liveVMs, vmID)
		}
		return err
	}
	live, known := f.liveVMs[vmID]
	if !known {
		return runner.ErrNotFound
	}
	if !live {
		return runner.ErrAlreadyTorndown
	}
	f.liveVMs[vmID] = false
	return nil
}

// BootCalls returns a snapshot of every BootCold input recorded.
func (f *Fake) BootCalls() []runner.BootInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.BootInput(nil), f.bootCalls...)
}

// LeaseCalls returns a snapshot of every LeaseHot input recorded.
func (f *Fake) LeaseCalls() []runner.LeaseInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.LeaseInput(nil), f.leaseCalls...)
}

// RunCalls returns a snapshot of every Run input recorded.
func (f *Fake) RunCalls() []runner.RunInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.RunInput(nil), f.runCalls...)
}

// TeardownCalls returns a snapshot of every Teardown vmID recorded.
func (f *Fake) TeardownCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.teardownCalls...)
}

// Compile-time assertion the fake satisfies the interface.
var _ runner.MicroVMProvisioner = (*Fake)(nil)

// ErrScriptedFailure is a convenience error for scripted failure paths.
var ErrScriptedFailure = errors.New("runnertest: scripted failure")
