// Package rpc is the public surface of plane/git. Callers (HTTP/SSH adapters,
// MCP server, etc.) depend on the GitRPC interface and never import the
// underlying Gitaly proto directly.
package rpc

import (
	"context"
	"io"

	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

// RepoRef re-exports gittypes.RepoRef so callers of the public GitRPC
// interface only need to import this package.
type RepoRef = gittypes.RepoRef

// RefUpdate re-exports gittypes.RefUpdate.
type RefUpdate = gittypes.RefUpdate

// GitRPC is the public Git surface of the platform.
//
// Implementations:
//   - GitalyProxy (production) — forwards over gRPC to a Gitaly file-server.
//
// All methods stream a Gitaly response body back to the caller as a
// ReadCloser; callers must Close the returned reader.
type GitRPC interface {
	// InfoRefs returns the smart-HTTP "info/refs" response for the given
	// service ("git-upload-pack" or "git-receive-pack").
	InfoRefs(ctx context.Context, repo RepoRef, service string) (io.ReadCloser, error)

	// UploadPack proxies a git-upload-pack (clone/fetch) exchange. r carries
	// the wire-format request body produced by the client.
	UploadPack(ctx context.Context, repo RepoRef, r io.Reader) (io.ReadCloser, error)

	// ReceivePack proxies a git-receive-pack (push) exchange. updates is the
	// parsed ref-update list, used by the pre-receive hook; r carries the
	// pack-stream body. The hook is invoked synchronously before the Gitaly
	// stream opens; a non-nil hook error rejects the push.
	ReceivePack(ctx context.Context, repo RepoRef, updates []RefUpdate, r io.Reader) (io.ReadCloser, error)
}
