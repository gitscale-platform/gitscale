package runner

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ResourceShape is the per-job resource envelope. The boot activity passes
// these into the Firecracker config; the in-VM template is responsible for
// honouring the egress cap via tc(8). Per spec D7, defaults come from a
// constants block in this package — never from the environment.
type ResourceShape struct {
	VCPU             int
	MemoryMB         int
	EgressKB         int64
	WallClockSeconds int
	EgressAllowlist  []string
}

// DefaultResourceShape is the v1 default envelope when the trigger HTTP
// route does not specify one. Matches the per-job ceiling on the free
// quota plan; production deploys may override at config time but the
// workflow never reads env.
var DefaultResourceShape = ResourceShape{
	VCPU:             2,
	MemoryMB:         2048,
	EgressKB:         512 * 1024, // 512 MiB
	WallClockSeconds: 1800,       // 30 min
	EgressAllowlist:  []string{"github.com:443", "git-proxy.gitscale.dev:443"},
}

// MicroVMHandle identifies a running (or just-allocated) microVM. ID is the
// Firecracker socket path basename and the idempotency anchor for
// TeardownVMActivity — the activity catches ErrAlreadyTorndown / ErrNotFound
// against this ID and returns success.
type MicroVMHandle struct {
	ID             string
	VsockCID       uint32
	IPv4           string
	KernelImage    string
	RootfsSnapshot string
}

// JobResult is the outcome of one in-VM job invocation. PeakMemoryKB and
// the byte counters are sourced from the Firecracker metrics endpoint by
// the runner adapter; the workflow plumbs them straight into the billing
// outbox event.
type JobResult struct {
	ExitCode       int
	DurationMS     int64
	BytesIngressed int64
	BytesEgressed  int64
	PeakMemoryKB   int64
	LogsObjectURI  string
}

// BootInput is the input to BootColdVMActivity.
type BootInput struct {
	JobID       uuid.UUID
	PrincipalID uuid.UUID
	OrgID       uuid.UUID
	Resource    ResourceShape
}

// LeaseInput is the input to LeaseHotVMActivity.
type LeaseInput struct {
	JobID       uuid.UUID
	PrincipalID uuid.UUID
	OrgID       uuid.UUID
	Resource    ResourceShape
}

// RunInput is the input to RunJobActivity.
type RunInput struct {
	VMID    string
	Command []string
	Env     map[string]string
	// EnvKeys is the deterministically sorted key slice for Env. The
	// workflow body computes it via sortedKeys before scheduling the
	// activity so the activity sees a stable iteration order regardless
	// of map insertion order in the worker.
	EnvKeys []string
}

// UsageInput is the input to EmitUsageEventActivity. Mirrors the outbox
// payload one-for-one (ADR-008) so downstream consumers see consistent
// fields irrespective of which code path emitted.
type UsageInput struct {
	JobID         uuid.UUID
	PrincipalID   uuid.UUID
	PrincipalKind string // "human" | "agent" | "service"
	OrgID         uuid.UUID
	RepoID        uuid.UUID
	Tier          string // "hot" | "cold"
	Result        JobResult
}

// MicroVMProvisioner is the seam between the runner activities and the
// Firecracker host. The default test path uses runnertest.Fake; the real
// implementation in microvm_firecracker.go is gated behind the
// firecracker_integration build tag.
//
// Implementations MUST:
//   - return ErrAlreadyTorndown / ErrNotFound from Teardown when the VM
//     is already gone (Temporal will retry; double-teardown must be safe);
//   - return ErrQuotaInsufficient when the requested ResourceShape exceeds
//     the host's per-job ceiling (independent of the billing-quota check
//     in the activity body — defense in depth);
//   - return ErrInvalidShape on malformed inputs (zero VCPU, etc.).
type MicroVMProvisioner interface {
	BootCold(ctx context.Context, in BootInput) (MicroVMHandle, error)
	LeaseHot(ctx context.Context, in LeaseInput) (MicroVMHandle, error)
	Run(ctx context.Context, in RunInput) (JobResult, error)
	Teardown(ctx context.Context, vmID string) error
}

// Sentinel errors. Boot activities classify ErrQuotaInsufficient and
// ErrInvalidShape as non-retryable; Teardown swallows ErrAlreadyTorndown
// and ErrNotFound as success.
var (
	// ErrQuotaInsufficient: the requested ResourceShape exceeds the
	// principal's per-job ceiling. Non-retryable; surface to caller.
	ErrQuotaInsufficient = errors.New("runner: requested resource shape exceeds quota")

	// ErrInvalidShape: malformed ResourceShape (zero VCPU / negative
	// memory). Non-retryable; programmer error in the trigger path.
	ErrInvalidShape = errors.New("runner: invalid resource shape")

	// ErrAlreadyTorndown: idempotent teardown signal. Caller treats as
	// success.
	ErrAlreadyTorndown = errors.New("runner: vm already torn down")

	// ErrNotFound: the host pool has no record of vmID. Treated as
	// success by Teardown — the desired state (gone) is achieved.
	ErrNotFound = errors.New("runner: vm not found")

	// ErrVMLost: surfaces from RunJobActivity when the host disappears
	// mid-execution. Non-retryable; CI jobs are not idempotent.
	ErrVMLost = errors.New("runner: vm lost during execution")
)
