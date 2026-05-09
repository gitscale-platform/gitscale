// Package runner owns the Firecracker microVM lifecycle for GitScale CI
// jobs as Temporal activities. It is the only sandbox surface for
// agent-submitted CI work (ADR-002) — Docker, gVisor, runc, runsc, podman
// and any container-runtime client are forbidden imports here. The
// hardware boundary is the threat model.
//
// The package exposes activities only — no workflow code lives in this
// package. The activities are composed by plane/workflow/ci.CIJobWorkflow,
// which is the single workflow function the worker registers on
// QueueCIPipelines (ADR-003).
//
// Side-effect surface (every entry is an activity, never a workflow body):
//
//   - BootColdVMActivity     boots a fresh microVM from a kernel + rootfs
//                            snapshot. Quota-checked at entry via
//                            appclient.BillingClient (ADR-019).
//   - LeaseHotVMActivity     leases a pre-warmed microVM from the hot
//                            fleet manager. Same quota check.
//   - RunJobActivity         issues a command set over vsock to the
//                            in-VM agent, streams logs, returns JobResult.
//   - TeardownVMActivity     idempotent teardown on MicroVMHandle.ID;
//                            ErrAlreadyTorndown / ErrNotFound are success.
//   - EmitUsageEventActivity emits ci.job_completed via the application
//                            plane (ADR-008/019). Workflow-plane never
//                            publishes to Kafka or writes the outbox
//                            directly.
//
// Real Firecracker integration (firecracker-go-sdk binding) is gated
// behind the //go:build firecracker_integration tag — default `go test
// ./...` runs against the in-memory MicroVMProvisioner fake from the
// runnertest sub-package. The SDK file is only compiled on hosts that
// have /dev/kvm and the operator-driven integration suite enabled.
//
// Plane boundary (ADR-019):
//
//   - This package may import plane/workflow/appclient/* (gRPC clients
//     into the application plane) and the Temporal SDK.
//   - It must NOT import plane/data/store, plane/data/cache, or any
//     plane/application/* package — quota reads and outbox writes are
//     routed via the gRPC client.
//
// ADR refs: ADR-002 (Firecracker isolation), ADR-003 (Temporal
// orchestration), ADR-008 (outbox), ADR-019 (workflow→app-plane RPC).
package runner
