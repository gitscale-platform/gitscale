package gitaly

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
)

// ErrBlobNotFound is returned when a requested blob is absent. The
// hook adapter's BlobReader contract treats wrapped instances of this
// error as "no blob" rather than "infra error".
var ErrBlobNotFound = errors.New("gitaly: blob not found")

// BlobReader is a Gitaly-backed BlobReader. It is structurally
// compatible with plane/application/agentsmd/hook.BlobReader; we do
// not import that package here so the plane boundary stays one-way
// (git → application is forbidden; application → git via the adapter is
// fine).
type BlobReader struct {
	pool    *client.GitalyPool
	locator locator.RepoLocator
}

// NewBlobReader constructs a Gitaly-backed BlobReader. Both arguments
// are required.
func NewBlobReader(pool *client.GitalyPool, loc locator.RepoLocator) *BlobReader {
	if pool == nil {
		panic("gitaly: nil pool")
	}
	if loc == nil {
		panic("gitaly: nil locator")
	}
	return &BlobReader{pool: pool, locator: loc}
}

// resolve maps a repoID to (Gitaly target address, gitalypb.Repository).
// Mirrors GitalyProxy.resolve; kept private to avoid public coupling.
func (b *BlobReader) resolve(ctx context.Context, repoID string) (string, *gitalypb.Repository, error) {
	addr, err := b.locator.Resolve(ctx, repoID)
	if err != nil {
		return "", nil, fmt.Errorf("gitaly: locator: %w", err)
	}
	repo := &gitalypb.Repository{
		StorageName:  addr.ReplicaSetID,
		RelativePath: repoID + ".git",
	}
	return addr.Addr, repo, nil
}

// ReadBlob streams TreeEntry for (revision, path) and concatenates the
// chunks. Returns ErrBlobNotFound when Gitaly reports the entry is
// missing or the streamed type is not a blob.
func (b *BlobReader) ReadBlob(ctx context.Context, repoID, ref, path string) ([]byte, error) {
	addr, repo, err := b.resolve(ctx, repoID)
	if err != nil {
		return nil, err
	}
	conn, err := b.pool.Conn(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("gitaly: dial: %w", err)
	}
	cl := gitalypb.NewCommitServiceClient(conn)
	stream, err := cl.TreeEntry(ctx, &gitalypb.TreeEntryRequest{
		Repository: repo,
		Revision:   []byte(ref),
		Path:       []byte(path),
	})
	if err != nil {
		return nil, fmt.Errorf("gitaly: tree_entry open: %w", err)
	}
	var (
		buf  bytes.Buffer
		seen bool
	)
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gitaly: tree_entry recv: %w", err)
		}
		if !seen {
			seen = true
			if resp.GetType() != gitalypb.TreeEntryResponse_BLOB {
				return nil, fmt.Errorf("%w: %s is not a blob (type=%s)", ErrBlobNotFound, path, resp.GetType().String())
			}
		}
		buf.Write(resp.GetData())
	}
	if !seen {
		return nil, fmt.Errorf("%w: %s at %s", ErrBlobNotFound, path, ref)
	}
	return buf.Bytes(), nil
}

// ListChangedPaths uses DiffService.FindChangedPaths to enumerate paths
// that differ between two commits. For a creation (oldOID == zero), the
// caller must pass the new tip; we approximate "all reachable paths" by
// asking Gitaly for the tree-vs-empty diff (Gitaly handles this when
// passed a single CommitRequest with the new commit, which diffs
// against the parent — for a root commit this surfaces every path).
func (b *BlobReader) ListChangedPaths(ctx context.Context, repoID, oldOID, newOID string) ([]string, error) {
	addr, repo, err := b.resolve(ctx, repoID)
	if err != nil {
		return nil, err
	}
	conn, err := b.pool.Conn(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("gitaly: dial: %w", err)
	}
	cl := gitalypb.NewDiffServiceClient(conn)

	req := &gitalypb.FindChangedPathsRequest{
		Repository: repo,
		Requests: []*gitalypb.FindChangedPathsRequest_Request{
			{
				Type: &gitalypb.FindChangedPathsRequest_Request_CommitRequest_{
					CommitRequest: &gitalypb.FindChangedPathsRequest_Request_CommitRequest{
						CommitRevision: newOID,
						ParentCommitRevisions: func() []string {
							if oldOID == "" || oldOID == "0000000000000000000000000000000000000000" {
								return nil
							}
							return []string{oldOID}
						}(),
					},
				},
			},
		},
	}
	stream, err := cl.FindChangedPaths(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gitaly: find_changed_paths open: %w", err)
	}
	var out []string
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gitaly: find_changed_paths recv: %w", err)
		}
		for _, p := range resp.GetPaths() {
			out = append(out, string(p.GetPath()))
		}
	}
	return out, nil
}

// BlobSize returns the byte size of the blob at (oid, path) by issuing
// a TreeEntry RPC with Limit=1 and reading Size from the first
// response message. We don't fetch the body.
func (b *BlobReader) BlobSize(ctx context.Context, repoID, oid, path string) (int64, error) {
	addr, repo, err := b.resolve(ctx, repoID)
	if err != nil {
		return 0, err
	}
	conn, err := b.pool.Conn(ctx, addr)
	if err != nil {
		return 0, fmt.Errorf("gitaly: dial: %w", err)
	}
	cl := gitalypb.NewCommitServiceClient(conn)
	stream, err := cl.TreeEntry(ctx, &gitalypb.TreeEntryRequest{
		Repository: repo,
		Revision:   []byte(oid),
		Path:       []byte(path),
		Limit:      1,
	})
	if err != nil {
		return 0, fmt.Errorf("gitaly: tree_entry open: %w", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("%w: %s at %s", ErrBlobNotFound, path, oid)
		}
		return 0, fmt.Errorf("gitaly: tree_entry recv: %w", err)
	}
	if resp.GetType() != gitalypb.TreeEntryResponse_BLOB {
		return 0, fmt.Errorf("%w: %s is not a blob", ErrBlobNotFound, path)
	}
	return resp.GetSize(), nil
}

// IsFastForward reports whether newOID is a (non-strict) descendant of
// oldOID. Equal OIDs are considered fast-forward.
func (b *BlobReader) IsFastForward(ctx context.Context, repoID, oldOID, newOID string) (bool, error) {
	if oldOID == newOID {
		return true, nil
	}
	addr, repo, err := b.resolve(ctx, repoID)
	if err != nil {
		return false, err
	}
	conn, err := b.pool.Conn(ctx, addr)
	if err != nil {
		return false, fmt.Errorf("gitaly: dial: %w", err)
	}
	cl := gitalypb.NewCommitServiceClient(conn)
	resp, err := cl.CommitIsAncestor(ctx, &gitalypb.CommitIsAncestorRequest{
		Repository: repo,
		AncestorId: oldOID,
		ChildId:    newOID,
	})
	if err != nil {
		return false, fmt.Errorf("gitaly: commit_is_ancestor: %w", err)
	}
	return resp.GetValue(), nil
}
