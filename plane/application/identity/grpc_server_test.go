package identity_test

import (
	"context"
	"net"
	"testing"
	"time"

	identityv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/identity/v1"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startStubServer(t *testing.T) (identityv1.IdentityServiceClient, func()) {
	t.Helper()
	svc := identity.NewStubService()
	srv := grpc.NewServer()
	identityv1.RegisterIdentityServiceServer(srv, identity.NewGRPCServer(svc))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	cleanup := func() {
		_ = cc.Close()
		srv.GracefulStop()
	}
	return identityv1.NewIdentityServiceClient(cc), cleanup
}

func TestGRPCServer_GetUser_NotFound(t *testing.T) {
	c, done := startStubServer(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.GetUser(ctx, &identityv1.GetUserRequest{Id: uuid.NewString()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if resp.GetFound() {
		t.Fatalf("expected not found")
	}
}

func TestGRPCServer_GetUser_InvalidUUID(t *testing.T) {
	c, done := startStubServer(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.GetUser(ctx, &identityv1.GetUserRequest{Id: "not-a-uuid"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPCServer_CreateUser_RoundTrip(t *testing.T) {
	c, done := startStubServer(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.CreateUser(ctx, &identityv1.CreateUserRequest{
		Email:               "alice@example.com",
		PlaintextCredential: "S3cret!1234",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if resp.GetUser().GetEmail() != "alice@example.com" {
		t.Fatalf("unexpected email: %q", resp.GetUser().GetEmail())
	}
	if _, err := uuid.Parse(resp.GetUser().GetId()); err != nil {
		t.Fatalf("non-uuid id: %v", err)
	}

	got, err := c.GetUser(ctx, &identityv1.GetUserRequest{Id: resp.GetUser().GetId()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !got.GetFound() || got.GetUser().GetEmail() != "alice@example.com" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestGRPCServer_DisableUser_NotFound(t *testing.T) {
	c, done := startStubServer(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.DisableUser(ctx, &identityv1.DisableUserRequest{
		UserId: uuid.NewString(),
		Reason: "spam",
	})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound for unknown user, got %s", st.Code())
	}
}

func TestGRPCServer_DisableUser_RoundTrip(t *testing.T) {
	c, done := startStubServer(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := c.CreateUser(ctx, &identityv1.CreateUserRequest{
		Email:               "bob@example.com",
		PlaintextCredential: "S3cret!1234",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := c.DisableUser(ctx, &identityv1.DisableUserRequest{
		UserId: created.GetUser().GetId(),
		Reason: "spam",
	}); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
}
