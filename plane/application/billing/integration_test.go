//go:build integration

package billing_test

import (
	"context"
	"net"
	"testing"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// TestE2E_AppclientToOutbox exercises the production path:
// workflow appclient -> gRPC server -> in-process Service -> postgres -> outbox.
// The test seeds two calls with the same natural key and asserts exactly one
// source row and one outbox row land — the ADR-008 invariant under retry.
func TestE2E_AppclientToOutbox(t *testing.T) {
	ctx := context.Background()
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := billing.NewPostgresService(ms)

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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	client := appclient.NewGRPCBillingClient(cc)

	in := appclient.PartitionArchivedInput{
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeURI:       "s3://lake/billing/usage_events/2026/05/",
		RowCount:      42,
		BytesWritten:  8192,
	}
	if err := client.RecordPartitionArchived(ctx, in); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := client.RecordPartitionArchived(ctx, in); err != nil {
		t.Fatalf("retry: %v", err)
	}

	var sourceCount, outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.partition_archives WHERE partition_name = $1`,
		in.PartitionName,
	).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1`,
		billing.EventTypePartitionArchived,
	).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || outboxCount != 1 {
		t.Fatalf("expected exactly 1 source / 1 outbox row, got %d / %d", sourceCount, outboxCount)
	}
}
