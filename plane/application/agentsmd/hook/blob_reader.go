package hook

import (
	"context"
	"errors"
)

// ErrBlobNotFound signals an absent blob (e.g. AGENTS.md missing).
// Distinct from infra errors so the handler can fail open on absence
// (treat as empty policy) and fail closed on infra errors.
var ErrBlobNotFound = errors.New("agentsmd/hook: blob not found")

// BlobReader is the bridge into plane/git for fetching blob bytes,
// listing changed paths, sizing blobs, and computing fast-forward
// ancestry. The production implementation lives in plane/git/gitaly
// (see GitalyBlobReader); tests inject in-memory implementations.
//
// Implementations must:
//   - return ErrBlobNotFound (or wrap it) when the requested blob does
//     not exist; any other error is treated as load-bearing infra
//     failure and the push is rejected with codes.Unavailable.
//   - be safe for concurrent use.
type BlobReader interface {
	// ReadBlob returns the raw bytes of <path> at <ref> (which can be a
	// commit OID, ref name like "HEAD", or short branch name) inside the
	// repository identified by repoID.
	ReadBlob(ctx context.Context, repoID, ref, path string) ([]byte, error)

	// ListChangedPaths returns the repository-relative paths whose blob
	// changed between oldOID and newOID. For a branch creation
	// (oldOID == ZeroOID per agentsmd), implementations should return
	// every path reachable from newOID.
	ListChangedPaths(ctx context.Context, repoID, oldOID, newOID string) ([]string, error)

	// BlobSize returns the size in bytes of <path> at <oid>.
	BlobSize(ctx context.Context, repoID, oid, path string) (int64, error)

	// IsFastForward reports whether newOID is a (non-strict) descendant
	// of oldOID in the repository.
	IsFastForward(ctx context.Context, repoID, oldOID, newOID string) (bool, error)
}
