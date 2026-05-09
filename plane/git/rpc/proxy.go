package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// receivePackChunkSize bounds a single PostReceivePack data frame. Picked to
// stay well under gRPC's default 4 MiB max-message-size while still
// amortising syscall overhead. A push of N bytes is sent as ceil(N/chunk)
// frames; the upstream stream sees them in order.
const receivePackChunkSize = 1 << 20 // 1 MiB

// GitalyProxy implements GitRPC by forwarding to a Gitaly file-server over
// gRPC. The pool, locator, hook, and metering counter are injected at
// construction; the proxy itself holds no other state.
//
// Stream semantics: each method opens a fresh Gitaly RPC bound to a child
// context derived from the caller's. The returned io.ReadCloser owns that
// child context; closing the reader cancels it, which tears down the
// upstream RPC even if Gitaly is still producing.
type GitalyProxy struct {
	pool    *client.GitalyPool
	locator locator.RepoLocator
	hook    hook.HookHandler
	meter   metering.Counter
}

// NewGitalyProxy constructs a proxy. All four arguments are required;
// pass hook.NoopHookHandler{} and metering.NewNoopCounter() to disable
// the corresponding behaviour.
func NewGitalyProxy(
	pool *client.GitalyPool,
	loc locator.RepoLocator,
	h hook.HookHandler,
	meter metering.Counter,
) *GitalyProxy {
	return &GitalyProxy{pool: pool, locator: loc, hook: h, meter: meter}
}

// resolve maps a RepoRef to (Gitaly target address, gitalypb.Repository).
// A locator miss is reported as gRPC NotFound; a real error is wrapped
// without translation so callers can inspect the underlying cause.
func (p *GitalyProxy) resolve(ctx context.Context, repoID string) (locator.FileServerAddr, *gitalypb.Repository, error) {
	addr, err := p.locator.Resolve(ctx, repoID)
	if err != nil {
		if errors.Is(err, locator.ErrRepoNotFound) {
			return locator.FileServerAddr{}, nil, status.Errorf(codes.NotFound, "git: repo %s not found", repoID)
		}
		return locator.FileServerAddr{}, nil, fmt.Errorf("git: locator: %w", err)
	}
	repo := &gitalypb.Repository{
		StorageName:  addr.ReplicaSetID,
		RelativePath: repoID + ".git",
	}
	return addr, repo, nil
}

// dial returns a gRPC client to the Gitaly file-server at addr, mapping
// dial failures to gRPC Unavailable.
func (p *GitalyProxy) dial(ctx context.Context, addr string) (gitalypb.SmartHTTPServiceClient, error) {
	conn, err := p.pool.Conn(ctx, addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "git: gitaly unavailable: %v", err)
	}
	return gitalypb.NewSmartHTTPServiceClient(conn), nil
}

// InfoRefs proxies the smart-HTTP info/refs response. service must be
// "git-upload-pack" (fetch) or "git-receive-pack" (push); any other value
// returns InvalidArgument.
//
// InfoRefs does NOT emit a metering event. The byte count cannot be
// observed without buffering the full response (defeating streaming), and
// a 0-byte event provides no signal that the edge-plane request counter
// (a separate tier upstream of this proxy) doesn't already carry. The
// reconciliation tier (ADR-015) tracks bytes for upload_pack and
// receive_pack only; InfoRefs is intentionally excluded so the analytics
// sink isn't dominated by zero-byte sentinel rows. See PR #120 self-review
// follow-up (c).
func (p *GitalyProxy) InfoRefs(ctx context.Context, ref RepoRef, service string) (io.ReadCloser, error) {
	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	cl, err := p.dial(ctx, addr.Addr)
	if err != nil {
		return nil, err
	}

	// Child context: closing the returned reader cancels the upstream RPC.
	streamCtx, cancel := context.WithCancel(ctx)
	req := &gitalypb.InfoRefsRequest{Repository: gRepo}

	var recv func() ([]byte, error)
	switch service {
	case "git-upload-pack":
		s, err := cl.InfoRefsUploadPack(streamCtx, req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("git: info_refs upload_pack: %w", err)
		}
		recv = func() ([]byte, error) {
			resp, err := s.Recv()
			if err != nil {
				return nil, err
			}
			return resp.GetData(), nil
		}
	case "git-receive-pack":
		s, err := cl.InfoRefsReceivePack(streamCtx, req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("git: info_refs receive_pack: %w", err)
		}
		recv = func() ([]byte, error) {
			resp, err := s.Recv()
			if err != nil {
				return nil, err
			}
			return resp.GetData(), nil
		}
	default:
		cancel()
		return nil, status.Errorf(codes.InvalidArgument, "git: info_refs: unknown service %q", service)
	}

	return streamToReader(recv, cancel), nil
}

// UploadPack proxies a git-upload-pack (fetch/clone) exchange.
//
// Gitaly v16 exposes upload-pack only via the sidechannel transport, which
// requires special connection wiring (gRPC sidechannel dialer + helper).
// Sidechannel support lands in a follow-up; this method currently returns
// Unimplemented so the GitRPC contract stays honest.
func (p *GitalyProxy) UploadPack(_ context.Context, _ RepoRef, _ io.Reader) (io.ReadCloser, error) {
	return nil, status.Error(codes.Unimplemented, "git: upload_pack not implemented (sidechannel transport)")
}

// ReceivePack proxies a git-receive-pack (push) exchange.
//
// Order of operations:
//  1. HookHandler.PreReceive — error rejects the push (PermissionDenied).
//  2. Locator.Resolve — error rejects with NotFound.
//  3. Pool.Conn — error rejects with Unavailable.
//  4. Open a bidi PostReceivePack stream; send (Repository, GlId,
//     GlRepository) header, then forward the body in bounded chunks
//     (receivePackChunkSize). Total bytes are accumulated for metering.
//  5. Record a metering event after the body has been forwarded.
//  6. Return an io.ReadCloser that streams Gitaly's response back; closing
//     it cancels the upstream RPC.
//
// A non-nil metering error rejects the push to preserve metering integrity
// (ADR-015). The Counter implementation decides which tier failures count
// as load-bearing; the proxy itself is policy-free.
//
// Bounded streaming (PR #120 self-review follow-up (a)): the body is
// forwarded chunk-by-chunk so a multi-GiB push does not allocate a
// matching in-memory buffer in the proxy. Each Send is a fresh
// PostReceivePackRequest with Data set to the chunk; the header message
// is sent once before the body.
func (p *GitalyProxy) ReceivePack(ctx context.Context, ref RepoRef, updates []RefUpdate, r io.Reader) (io.ReadCloser, error) {
	if err := p.hook.PreReceive(ctx, ref, updates); err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "git: pre-receive hook: %v", err)
	}

	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	cl, err := p.dial(ctx, addr.Addr)
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := cl.PostReceivePack(streamCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("git: receive_pack open: %w", err)
	}

	// First message: header carrying repository metadata + caller identity.
	if err := stream.Send(&gitalypb.PostReceivePackRequest{
		Repository:   gRepo,
		GlId:         ref.AgentID,
		GlRepository: ref.RepoID,
	}); err != nil {
		cancel()
		return nil, fmt.Errorf("git: receive_pack header: %w", err)
	}

	// Forward the pack body in bounded chunks.
	totalBytes, err := forwardChunks(r, stream)
	if err != nil {
		cancel()
		return nil, err
	}

	if err := stream.CloseSend(); err != nil {
		cancel()
		return nil, fmt.Errorf("git: receive_pack close-send: %w", err)
	}

	// Metering after the body is on the wire. A non-nil error here is
	// load-bearing — the push is rejected to preserve outbox integrity.
	// PackObjects is unknown at this layer; precise attribution lands when
	// the Gitaly custom hook ships in Phase 3.
	if err := p.meter.Record(ctx, ref, metering.OpReceivePack, totalBytes, 0, len(updates)); err != nil {
		cancel()
		return nil, fmt.Errorf("git: receive_pack metering: %w", err)
	}

	return streamToReader(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return resp.GetData(), nil
	}, cancel), nil
}

// forwardChunks reads from r and forwards each chunk as a separate
// PostReceivePackRequest. Returns total bytes forwarded.
func forwardChunks(r io.Reader, stream gitalypb.SmartHTTPService_PostReceivePackClient) (int64, error) {
	buf := make([]byte, receivePackChunkSize)
	var total int64
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			// Copy because gRPC retains the slice until Send returns and
			// the next Read overwrites buf.
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if serr := stream.Send(&gitalypb.PostReceivePackRequest{Data: chunk}); serr != nil {
				return total, fmt.Errorf("git: receive_pack data: %w", serr)
			}
			total += int64(n)
		}
		if errors.Is(rerr, io.EOF) {
			return total, nil
		}
		if rerr != nil {
			return total, fmt.Errorf("git: receive_pack body: %w", rerr)
		}
	}
}

// streamToReader converts a chunk-pull function into an io.ReadCloser.
// next returns (nil, io.EOF) at end-of-stream; a non-EOF error is
// propagated to the reader as CloseWithError.
//
// Lifecycle hardening (PR #120 self-review follow-up (b)): cancel is
// invoked exactly once — either when next signals end-of-stream, when the
// caller closes the returned reader, or when next surfaces an error. This
// guarantees the upstream gRPC stream is torn down even if the caller
// abandons the reader without draining it.
func streamToReader(next func() ([]byte, error), cancel context.CancelFunc) io.ReadCloser {
	pr, pw := io.Pipe()
	closer := &cancelCloser{PipeReader: pr, cancel: cancel}

	go func() {
		defer func() {
			_ = pw.Close()
			closer.fire() // tear down upstream when the producer exits
		}()
		for {
			chunk, err := next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, werr := pw.Write(chunk); werr != nil {
				// Caller closed the reader (PipeError) — abandon the
				// upstream stream; cancel will fire in the deferred close.
				return
			}
		}
	}()
	return closer
}

// cancelCloser wraps an io.PipeReader so closing it cancels the upstream
// gRPC stream. cancel is fired at most once across both the caller's
// Close path and the producer goroutine's exit; sync.Once handles the
// race when both fire simultaneously.
type cancelCloser struct {
	*io.PipeReader
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelCloser) fire() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
}

func (c *cancelCloser) Close() error {
	err := c.PipeReader.Close()
	c.fire()
	return err
}

// Compile-time check that GitalyProxy implements GitRPC.
var _ GitRPC = (*GitalyProxy)(nil)
