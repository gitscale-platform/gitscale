package gitaly_test

// We assert that gitaly.BlobReader satisfies the agentsmd hook
// adapter's BlobReader interface — structurally, via assignment to a
// variable of the interface type. This keeps the plane boundary
// one-way (the hook package is in plane/application; importing it
// here is permitted only for tests, which are compiled in a separate
// package).
//
// Live RPC tests against a Gitaly testcontainer are deferred until
// the cmd/git-rpc binary lands; the integration smoke for the AGENTS.md
// chain lives in plane/application/agentsmd/integration_test.go.

import (
	"testing"

	hookpkg "github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
	"github.com/gitscale-platform/gitscale/plane/git/gitaly"
)

func TestBlobReader_StructurallyImplementsAdapterInterface(t *testing.T) {
	// Compile-time assertion: gitaly.BlobReader satisfies hookpkg.BlobReader.
	var _ hookpkg.BlobReader = (*gitaly.BlobReader)(nil)
}
