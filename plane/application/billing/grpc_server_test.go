package billing_test

import (
	"context"
	"net"
	"testing"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// bufServer boots an in-process gRPC server backed by svc and returns a
// connected client. Cleanup stops the server and closes the connection.
func bufServer(t *testing.T, svc billing.Service) billingv1.BillingServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(srv, billing.NewGRPCServer(svc))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return billingv1.NewBillingServiceClient(cc)
}

func validRequest() *billingv1.RecordPartitionArchivedRequest {
	return &billingv1.RecordPartitionArchivedRequest{
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeUri:       "s3://lake/",
		RowCount:      1,
		BytesWritten:  100,
	}
}

func TestGRPC_RecordPartitionArchived_HappyAndIdempotent(t *testing.T) {
	c := bufServer(t, billing.NewStubService())
	first, err := c.RecordPartitionArchived(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first.GetCreated() {
		t.Fatal("expected Created on first")
	}
	if first.GetArchiveId() == "" {
		t.Fatal("expected non-empty archive_id")
	}

	second, err := c.RecordPartitionArchived(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.GetCreated() {
		t.Fatal("expected Created=false on retry")
	}
	if second.GetArchiveId() != first.GetArchiveId() {
		t.Fatalf("archive_id changed across retry: %s -> %s", first.GetArchiveId(), second.GetArchiveId())
	}
}

func TestGRPC_ValidationErrors_MapToInvalidArgument(t *testing.T) {
	c := bufServer(t, billing.NewStubService())
	cases := []struct {
		name string
		mut  func(*billingv1.RecordPartitionArchivedRequest)
	}{
		{"year-low", func(r *billingv1.RecordPartitionArchivedRequest) { r.Year = 2025 }},
		{"year-high", func(r *billingv1.RecordPartitionArchivedRequest) { r.Year = 2101 }},
		{"month-zero", func(r *billingv1.RecordPartitionArchivedRequest) { r.Month = 0 }},
		{"month-thirteen", func(r *billingv1.RecordPartitionArchivedRequest) { r.Month = 13 }},
		{"empty-name", func(r *billingv1.RecordPartitionArchivedRequest) { r.PartitionName = "" }},
		{"empty-uri", func(r *billingv1.RecordPartitionArchivedRequest) { r.LakeUri = "" }},
		{"negative-rows", func(r *billingv1.RecordPartitionArchivedRequest) { r.RowCount = -1 }},
		{"negative-bytes", func(r *billingv1.RecordPartitionArchivedRequest) { r.BytesWritten = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mut(req)
			_, err := c.RecordPartitionArchived(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status, got %v", err)
			}
			if st.Code() != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %v (%v)", st.Code(), err)
			}
		})
	}
}
