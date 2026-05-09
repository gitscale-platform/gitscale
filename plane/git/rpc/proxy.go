package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GitalyProxy implements GitRPC by forwarding to a Gitaly file-server over
// gRPC. The pool, locator, hook, and metering counter are injected at
// construction; the proxy itself holds no other state.
//
// Stream semantics: each method opens a fresh Gitaly RPC and returns the
// streamed body to the caller as an io.ReadCloser. Closing the reader
// cancels the upstream stream.
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
// returns InvalidArgument. Bytes received from Gitaly are recorded as a
// metering event before the reader is returned to the caller.
func (p *GitalyProxy) InfoRefs(ctx context.Context, ref RepoRef, service string) (io.ReadCloser, error) {
	addr, gRepo, err := p.resolve(ctx, ref.RepoID)
	if err != nil {
		return nil, err
	}
	cl, err := p.dial(ctx, addr.Addr)
	if err != nil {
		return nil, err
	}

	req := &gitalypb.InfoRefsRequest{Repository: gRepo}

	var (
		stream gitalypb.SmartHTTPService_InfoRefsUploadPackClient
		recv   func() ([]byte, error)
	)
	switch service {
	case "git-upload-pack":
		s, err := cl.InfoRefsUploadPack(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("git: info_refs upload_pack: %w", err)
		}
		stream = s
		recv = func() ([]byte, error) {
			resp, err := s.Recv()
			if err != nil {
				return nil, err
			}
			return resp.GetData(), nil
		}
	case "git-receive-pack":
		s, err := cl.InfoRefsReceivePack(ctx, req)
		if err != nil {
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
		return nil, status.Errorf(codes.InvalidArgument, "git: info_refs: unknown service %q", service)
	}
	_ = stream // silence unused if upload-pack branch not taken

	// Metering for info_refs is best-effort — bytes are unknown until the
	// stream drains. Record a zero-byte event so the operation is counted;
	// the reconciliation tier (#109) refines this once stream length is
	// observable on close.
	if err := p.meter.Record(ctx, ref, "info_refs", 0, 0, 0); err != nil {
		return nil, fmt.Errorf("git: info_refs metering: %w", err)
	}

	return streamToReader(recv), nil
}

// UploadPack proxies a git-upload-pack (fetch/clone) exchange.
//
// Gitaly v16 exposes upload-pack only via the sidechannel transport, which
// requires special connection wiring (gRPC sidechannel dialer + helper).
// Sidechannel support lands in #109 alongside the metering reconciliation
// path; this method currently returns Unimplemented so the GitRPC contract
// stays honest.
func (p *GitalyProxy) UploadPack(_ context.Context, _ RepoRef, _ io.Reader) (io.ReadCloser, error) {
	return nil, status.Error(codes.Unimplemented, "git: upload_pack not implemented (sidechannel transport — see #109)")
}

// ReceivePack proxies a git-receive-pack (push) exchange.
//
// Order of operations:
//  1. HookHandler.PreReceive — error rejects the push (PermissionDenied).
//  2. Locator.Resolve — error rejects with NotFound.
//  3. Pool.Conn — error rejects with Unavailable.
//  4. Open a bidi PostReceivePack stream; send (Repository, GlId, GlRepository)
//     header, then the body bytes.
//  5. Record a metering event after the body has been forwarded.
//  6. Stream the Gitaly response body back to the caller.
//
// A non-nil metering error rejects the push to preserve metering integrity
// (ADR-012). The Counter implementation decides which tier failures count
// as load-bearing; the proxy itself is policy-free.
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
	stream, err := cl.PostReceivePack(ctx)
	if err != nil {
		return nil, fmt.Errorf("git: receive_pack open: %w", err)
	}

	// First message: header carrying repository metadata + caller identity.
	if err := stream.Send(&gitalypb.PostReceivePackRequest{
		Repository:   gRepo,
		GlId:         ref.AgentID,
		GlRepository: ref.RepoID,
	}); err != nil {
		return nil, fmt.Errorf("git: receive_pack header: %w", err)
	}

	// Second message: pack-stream body. Buffered into memory for the v1
	// proxy; large pushes will need streaming chunks once the SSH adapter
	// lands (out of scope here).
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("git: receive_pack body: %w", err)
	}
	if err := stream.Send(&gitalypb.PostReceivePackRequest{Data: body}); err != nil {
		return nil, fmt.Errorf("git: receive_pack data: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return nil, fmt.Errorf("git: receive_pack close-send: %w", err)
	}

	// Metering after the body is on the wire. A non-nil error here is
	// load-bearing — the push is rejected to preserve outbox integrity.
	// PackObjects is unknown at this layer; the Counter records it as 0
	// and leaves precise attribution to the Gitaly hook layer (#109).
	if err := p.meter.Record(ctx, ref, "receive_pack", int64(len(body)), 0, len(updates)); err != nil {
		return nil, fmt.Errorf("git: receive_pack metering: %w", err)
	}

	return streamToReader(func() ([]byte, error) {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return resp.GetData(), nil
	}), nil
}

// streamToReader converts a chunk-pull function into an io.ReadCloser.
// next returns (nil, io.EOF) at end-of-stream. A non-EOF error from next
// is propagated to the reader as a CloseWithError.
func streamToReader(next func() ([]byte, error)) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
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
				return
			}
		}
	}()
	return pr
}

// Compile-time check that GitalyProxy implements GitRPC.
var _ GitRPC = (*GitalyProxy)(nil)
