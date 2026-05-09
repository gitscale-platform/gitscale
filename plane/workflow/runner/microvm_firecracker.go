//go:build firecracker_integration

// firecracker_integration build tag: real Firecracker SDK binding.
//
// This file is compiled only on hosts with /dev/kvm and the operator-driven
// integration suite enabled. Default `go test ./...` ignores it.
//
// Wiring against firecracker-microvm/firecracker-go-sdk lives here. The
// constructor accepts the path to the firecracker binary, kernel image,
// and rootfs snapshot; per-job tweaks (vCPU, memory, vsock CID) come
// from BootInput / LeaseInput. The implementation is a follow-up issue
// (#TODO link) — until then this file is a compile-only placeholder so
// the build tag exists and the lint can assert "import surface stays
// inside one tagged file".
//
// ADR-002 forbids any container-runtime client in this package. The lint
// scanner enforces the prohibition; see plane/workflow/lint/firecracker-rules.txt
// for the canonical list.
package runner

// FirecrackerProvisioner is a placeholder for the real provisioner. The
// real implementation must satisfy MicroVMProvisioner; the stub below is
// here so the build tag compiles cleanly when set, and so the structure
// of the follow-up issue is fixed.
type FirecrackerProvisioner struct {
	// FirecrackerBinaryPath, KernelImage, RootfsSnapshot are configured
	// at construction time; per-job tweaks come from BootInput.
	FirecrackerBinaryPath string
	KernelImage           string
	RootfsSnapshot        string
}
