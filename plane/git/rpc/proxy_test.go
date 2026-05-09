package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
	"github.com/gitscale-platform/gitscale/plane/git/hook"
	"github.com/gitscale-platform/gitscale/plane/git/locator"
	"github.com/gitscale-platform/gitscale/plane/git/metering"
	gitrpc "github.com/gitscale-platform/gitscale/plane/git/rpc"
	"github.com/stretchr/testify/require"
	gitalypb "gitlab.com/gitlab-org/gitaly/v16/proto/go/gitalypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeGitalyServer is a minimal SmartHTTPService implementation for testing.
// Each test instantiates one and registers it on a freshly-bound listener.
type fakeGitalyServer struct {
	gitalypb.UnimplementedSmartHTTPServiceServer

	infoRefsUploadData  []byte
	infoRefsReceiveData []byte
	receivePackResponse []byte

	// Captured on the most recent call, used by assertions.
	receivedHeader *gitalypb.PostReceivePackRequest
	receivedBody   []byte
}

func (s *fakeGitalyServer) InfoRefsUploadPack(_ *gitalypb.InfoRefsRequest, stream gitalypb.SmartHTTPService_InfoRefsUploadPackServer) error {
	return stream.Send(&gitalypb.InfoRefsResponse{Data: s.infoRefsUploadData})
}

func (s *fakeGitalyServer) InfoRefsReceivePack(_ *gitalypb.InfoRefsRequest, stream gitalypb.SmartHTTPService_InfoRefsReceivePackServer) error {
	return stream.Send(&gitalypb.InfoRefsResponse{Data: s.infoRefsReceiveData})
}

func (s *fakeGitalyServer) PostReceivePack(stream gitalypb.SmartHTTPService_PostReceivePackServer) error {
	header, err := stream.Recv()
	if err != nil {
		return err
	}
	s.receivedHeader = header
	body, err := stream.Recv()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if body != nil {
		s.receivedBody = body.GetData()
	}
	return stream.Send(&gitalypb.PostReceivePackResponse{Data: s.receivePackResponse})
}

// startFakeGitaly registers fake on a random port and returns its address.
// The server stops when the test finishes.
func startFakeGitaly(t *testing.T, fake *fakeGitalyServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	gitalypb.RegisterSmartHTTPServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// fixedAddrLocator returns the same FileServerAddr for every Resolve call.
type fixedAddrLocator struct {
	addr locator.FileServerAddr
}

func (f *fixedAddrLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return f.addr, nil
}

// notFoundLocator always returns ErrRepoNotFound.
type notFoundLocator struct{}

func (n *notFoundLocator) Resolve(_ context.Context, _ string) (locator.FileServerAddr, error) {
	return locator.FileServerAddr{}, locator.ErrRepoNotFound
}

// rejectHook rejects every push with the given message.
type rejectHook struct{ msg string }

func (h *rejectHook) PreReceive(_ context.Context, _ gittypes.RepoRef, _ []gittypes.RefUpdate) error {
	return errors.New(h.msg)
}

// failingMeter returns the configured error. Used to verify metering errors
// propagate from ReceivePack.
type failingMeter struct{ err error }

func (f *failingMeter) Record(_ context.Context, _ gittypes.RepoRef, _ string, _ int64, _ int64, _ int) error {
	return f.err
}

// recordingMeter captures every Record call for assertion.
type recordingMeter struct {
	calls []recordingMeterCall
}

type recordingMeterCall struct {
	op          string
	bytes       int64
	packObjects int64
	refUpdates  int
	agentID     string
}

func (r *recordingMeter) Record(_ context.Context, ref gittypes.RepoRef, op string, b, p int64, ru int) error {
	r.calls = append(r.calls, recordingMeterCall{
		op: op, bytes: b, packObjects: p, refUpdates: ru, agentID: ref.AgentID,
	})
	return nil
}

// newProxy builds a GitalyProxy pointed at gitalyAddr with NoopHookHandler
// and NoopCounter. Caller may swap the hook or counter via the returned
// pointer's options if the test variant requires it.
func newProxy(t *testing.T, gitalyAddr string, h hook.HookHandler, m metering.Counter) gitrpc.GitRPC {
	t.Helper()
	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)
	loc := &fixedAddrLocator{addr: locator.FileServerAddr{
		ReplicaSetID: "test-rs",
		HomeRegion:   "us-test",
		Addr:         gitalyAddr,
	}}
	return gitrpc.NewGitalyProxy(pool, loc, h, m)
}

func TestProxy_InfoRefs_UploadPack(t *testing.T) {
	fake := &fakeGitalyServer{infoRefsUploadData: []byte("# service=git-upload-pack\n0000")}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, metering.NewNoopCounter())

	rc, err := proxy.InfoRefs(context.Background(), gittypes.RepoRef{RepoID: "r"}, "git-upload-pack")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, fake.infoRefsUploadData, got)
}

func TestProxy_InfoRefs_ReceivePack(t *testing.T) {
	fake := &fakeGitalyServer{infoRefsReceiveData: []byte("# service=git-receive-pack\n0000")}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, metering.NewNoopCounter())

	rc, err := proxy.InfoRefs(context.Background(), gittypes.RepoRef{RepoID: "r"}, "git-receive-pack")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, fake.infoRefsReceiveData, got)
}

func TestProxy_InfoRefs_UnknownService(t *testing.T) {
	fake := &fakeGitalyServer{}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, metering.NewNoopCounter())

	_, err := proxy.InfoRefs(context.Background(), gittypes.RepoRef{RepoID: "r"}, "git-bogus")
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestProxy_LocatorNotFound(t *testing.T) {
	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)
	proxy := gitrpc.NewGitalyProxy(pool, &notFoundLocator{}, hook.NoopHookHandler{}, metering.NewNoopCounter())

	_, err := proxy.InfoRefs(context.Background(), gittypes.RepoRef{RepoID: "missing"}, "git-upload-pack")
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestProxy_UploadPack_Unimplemented(t *testing.T) {
	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)
	loc := &fixedAddrLocator{addr: locator.FileServerAddr{Addr: "127.0.0.1:1"}}
	proxy := gitrpc.NewGitalyProxy(pool, loc, hook.NoopHookHandler{}, metering.NewNoopCounter())
	_, err := proxy.UploadPack(context.Background(), gittypes.RepoRef{RepoID: "r"}, bytes.NewReader(nil))
	require.Error(t, err)
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestProxy_ReceivePack_NoHook(t *testing.T) {
	fake := &fakeGitalyServer{receivePackResponse: []byte("ok")}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, metering.NewNoopCounter())

	updates := []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "0000", NewOID: "abcd"},
	}
	rc, err := proxy.ReceivePack(
		context.Background(),
		gittypes.RepoRef{RepoID: "r", AgentID: "agent-1"},
		updates,
		bytes.NewReader([]byte("packdata")),
	)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), got)

	require.NotNil(t, fake.receivedHeader)
	require.Equal(t, "agent-1", fake.receivedHeader.GetGlId())
	require.Equal(t, "r", fake.receivedHeader.GetGlRepository())
	require.Equal(t, []byte("packdata"), fake.receivedBody)
}

func TestProxy_ReceivePack_HookRejects(t *testing.T) {
	fake := &fakeGitalyServer{}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, &rejectHook{msg: "no pushes to main"}, metering.NewNoopCounter())

	_, err := proxy.ReceivePack(
		context.Background(),
		gittypes.RepoRef{RepoID: "r"},
		[]gittypes.RefUpdate{{RefName: "refs/heads/main"}},
		bytes.NewReader(nil),
	)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, err.Error(), "no pushes to main")

	// Hook rejection happens before the upstream stream opens — the fake
	// server must not have observed the request.
	require.Nil(t, fake.receivedHeader)
}

func TestProxy_ReceivePack_MeteringErrorPropagates(t *testing.T) {
	fake := &fakeGitalyServer{receivePackResponse: []byte("ok")}
	addr := startFakeGitaly(t, fake)
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, &failingMeter{err: errors.New("outbox: db down")})

	_, err := proxy.ReceivePack(
		context.Background(),
		gittypes.RepoRef{RepoID: "r"},
		nil,
		bytes.NewReader([]byte("data")),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outbox: db down")
}

func TestProxy_ReceivePack_RecordsMeteringEvent(t *testing.T) {
	fake := &fakeGitalyServer{receivePackResponse: []byte("ok")}
	addr := startFakeGitaly(t, fake)
	rec := &recordingMeter{}
	proxy := newProxy(t, addr, hook.NoopHookHandler{}, rec)

	updates := []gittypes.RefUpdate{
		{RefName: "refs/heads/main"},
		{RefName: "refs/heads/dev"},
	}
	rc, err := proxy.ReceivePack(
		context.Background(),
		gittypes.RepoRef{RepoID: "r", AgentID: "agent-x"},
		updates,
		bytes.NewReader([]byte("0123456789")),
	)
	require.NoError(t, err)
	_, _ = io.ReadAll(rc)
	_ = rc.Close()

	require.Len(t, rec.calls, 1)
	require.Equal(t, "receive_pack", rec.calls[0].op)
	require.Equal(t, int64(10), rec.calls[0].bytes)
	require.Equal(t, 2, rec.calls[0].refUpdates)
	require.Equal(t, "agent-x", rec.calls[0].agentID)
}
