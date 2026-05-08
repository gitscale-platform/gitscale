package appclient

import (
	"context"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"google.golang.org/grpc"
)

// grpcBillingClient implements BillingClient against a generated
// BillingServiceClient. Per ADR-019, RecordPartitionArchived is a single
// unary RPC into cmd/billing-service which performs the source-row +
// outbox-row write in one Tx (ADR-008). This adapter holds no state; the
// caller owns the underlying *grpc.ClientConn lifecycle.
type grpcBillingClient struct {
	c billingv1.BillingServiceClient
}

// NewGRPCBillingClient returns a BillingClient backed by an existing gRPC
// client connection. The connection lifecycle is owned by the caller.
func NewGRPCBillingClient(cc *grpc.ClientConn) BillingClient {
	return &grpcBillingClient{c: billingv1.NewBillingServiceClient(cc)}
}

// RecordPartitionArchived calls into the billing app-plane service. Both new
// inserts and idempotent retries return nil from the workflow's perspective;
// the workflow does not care which path it took, only that the event is
// guaranteed to land at most once in the outbox.
func (g *grpcBillingClient) RecordPartitionArchived(ctx context.Context, in PartitionArchivedInput) error {
	_, err := g.c.RecordPartitionArchived(ctx, &billingv1.RecordPartitionArchivedRequest{
		Year:          int32(in.Year),
		Month:         int32(in.Month),
		PartitionName: in.PartitionName,
		LakeUri:       in.LakeURI,
		RowCount:      in.RowCount,
		BytesWritten:  in.BytesWritten,
	})
	return err
}
