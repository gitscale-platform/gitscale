package appclient

import (
	"context"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// RecordDEKDestroyed calls into the billing app-plane service to emit
// billing.partition_dek_destroyed. Idempotent retries return nil; only the
// first call writes the outbox row (anchored on the deterministic UUIDv5
// aggregate_id derived from the natural key).
func (g *grpcBillingClient) RecordDEKDestroyed(ctx context.Context, in DEKDestroyedInput) error {
	_, err := g.c.RecordDEKDestroyed(ctx, &billingv1.RecordDEKDestroyedRequest{
		Year:            int32(in.Year),
		Month:           int32(in.Month),
		PartitionName:   in.PartitionName,
		KekHint:         in.KEKHint,
		VaultKeyVersion: int32(in.VaultKeyVersion),
	})
	return err
}

// GetQuotaAccount fetches the per-org quota envelope. NotFound
// (codes.NotFound) is translated to ErrQuotaAccountNotFound so the
// workflow plane can classify the error without depending on grpc/codes.
func (g *grpcBillingClient) GetQuotaAccount(ctx context.Context, orgID uuid.UUID) (QuotaAccountView, error) {
	resp, err := g.c.GetQuotaAccount(ctx, &billingv1.GetQuotaAccountRequest{
		OrgId: orgID.String(),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return QuotaAccountView{}, ErrQuotaAccountNotFound
		}
		return QuotaAccountView{}, err
	}
	accountID, err := uuid.Parse(resp.GetAccountId())
	if err != nil {
		return QuotaAccountView{}, err
	}
	respOrgID, err := uuid.Parse(resp.GetOrgId())
	if err != nil {
		return QuotaAccountView{}, err
	}
	return QuotaAccountView{
		AccountID:                 accountID,
		OrgID:                     respOrgID,
		PlanTier:                  resp.GetPlanTier(),
		TokensPerWeekCap:          resp.GetTokensPerWeekCap(),
		ComputeMinutesPerMonthCap: resp.GetComputeMinutesPerMonthCap(),
		StorageGBCap:              resp.GetStorageGbCap(),
	}, nil
}

// RecordCIJobCompleted emits ci.job_completed via the billing app-plane
// service. Idempotent on job_id (#110, ADR-008/019). Repeat calls return
// nil from the workflow's perspective regardless of whether the outbox row
// was newly written or deduped.
func (g *grpcBillingClient) RecordCIJobCompleted(ctx context.Context, in CIJobCompletedInput) error {
	_, err := g.c.RecordCIJobCompleted(ctx, &billingv1.RecordCIJobCompletedRequest{
		JobId:           in.JobID.String(),
		PrincipalId:     in.PrincipalID.String(),
		PrincipalKind:   in.PrincipalKind,
		OrgId:           in.OrgID.String(),
		RepoId:          in.RepoID.String(),
		Tier:            in.Tier,
		VcpuSeconds:     in.VCPUSeconds,
		MemoryMbSeconds: in.MemoryMBSeconds,
		EgressKb:        in.EgressKB,
		ExitCode:        int32(in.ExitCode),
	})
	return err
}
