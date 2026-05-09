package billing

import (
	"context"
	"errors"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer adapts the in-process Service to the generated BillingService
// gRPC surface (ADR-019). It owns no state beyond the wrapped Service; the
// underlying Service ensures source row + outbox row land in the same Tx
// (ADR-008).
type GRPCServer struct {
	billingv1.UnimplementedBillingServiceServer
	svc Service
}

// NewGRPCServer wraps svc.
func NewGRPCServer(svc Service) *GRPCServer {
	return &GRPCServer{svc: svc}
}

// RecordPartitionArchived translates the proto request into the service-level
// input and maps domain errors to the documented gRPC code surface.
func (s *GRPCServer) RecordPartitionArchived(ctx context.Context, req *billingv1.RecordPartitionArchivedRequest) (*billingv1.RecordPartitionArchivedResponse, error) {
	out, err := s.svc.RecordPartitionArchived(ctx, RecordPartitionArchivedInput{
		Year:          int(req.GetYear()),
		Month:         int(req.GetMonth()),
		PartitionName: req.GetPartitionName(),
		LakeURI:       req.GetLakeUri(),
		RowCount:      req.GetRowCount(),
		BytesWritten:  req.GetBytesWritten(),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &billingv1.RecordPartitionArchivedResponse{
		ArchiveId: out.ArchiveID,
		Created:   out.Created,
	}, nil
}

// RecordDEKDestroyed translates the proto request into the service-level
// input and maps domain errors to the documented gRPC code surface.
func (s *GRPCServer) RecordDEKDestroyed(ctx context.Context, req *billingv1.RecordDEKDestroyedRequest) (*billingv1.RecordDEKDestroyedResponse, error) {
	out, err := s.svc.RecordDEKDestroyed(ctx, RecordDEKDestroyedInput{
		Year:            int(req.GetYear()),
		Month:           int(req.GetMonth()),
		PartitionName:   req.GetPartitionName(),
		KEKHint:         req.GetKekHint(),
		VaultKeyVersion: int(req.GetVaultKeyVersion()),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &billingv1.RecordDEKDestroyedResponse{
		EventId: out.EventID,
		Created: out.Created,
	}, nil
}

// GetQuotaAccount maps the proto request to the service input and returns
// the org-level quota envelope. NotFound is the canonical surface for "no
// quota row" so the workflow plane's gRPC client can translate to
// appclient.ErrQuotaAccountNotFound (#110, ADR-019).
func (s *GRPCServer) GetQuotaAccount(ctx context.Context, req *billingv1.GetQuotaAccountRequest) (*billingv1.GetQuotaAccountResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid org_id: "+err.Error())
	}
	out, err := s.svc.GetQuotaAccount(ctx, GetQuotaAccountInput{OrgID: orgID})
	if err != nil {
		return nil, mapErr(err)
	}
	return &billingv1.GetQuotaAccountResponse{
		AccountId:                 out.AccountID.String(),
		OrgId:                     out.OrgID.String(),
		PlanTier:                  out.PlanTier,
		TokensPerWeekCap:          out.TokensPerWeekCap,
		ComputeMinutesPerMonthCap: out.ComputeMinutesPerMonthCap,
		StorageGbCap:              out.StorageGBCap,
	}, nil
}

// RecordCIJobCompleted translates the proto request into the service-level
// input and emits ci.job_completed (#110, ADR-008/019). Idempotent on
// JobID; repeat calls return Created=false.
func (s *GRPCServer) RecordCIJobCompleted(ctx context.Context, req *billingv1.RecordCIJobCompletedRequest) (*billingv1.RecordCIJobCompletedResponse, error) {
	jobID, err := uuid.Parse(req.GetJobId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid job_id: "+err.Error())
	}
	principalID, err := uuid.Parse(req.GetPrincipalId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid principal_id: "+err.Error())
	}
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid org_id: "+err.Error())
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid repo_id: "+err.Error())
	}
	out, err := s.svc.RecordCIJobCompleted(ctx, RecordCIJobCompletedInput{
		JobID:           jobID,
		PrincipalID:     principalID,
		PrincipalKind:   req.GetPrincipalKind(),
		OrgID:           orgID,
		RepoID:          repoID,
		Tier:            req.GetTier(),
		VCPUSeconds:     req.GetVcpuSeconds(),
		MemoryMBSeconds: req.GetMemoryMbSeconds(),
		EgressKB:        req.GetEgressKb(),
		ExitCode:        int(req.GetExitCode()),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &billingv1.RecordCIJobCompletedResponse{
		EventId: out.EventID,
		Created: out.Created,
	}, nil
}

// mapErr converts a billing domain error into a gRPC status. Validation
// failures map to InvalidArgument; quota-account-not-found maps to
// NotFound; unknown errors are reported as Internal.
func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrQuotaAccountNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrInvalidYear),
		errors.Is(err, ErrInvalidMonth),
		errors.Is(err, ErrEmptyPartitionName),
		errors.Is(err, ErrEmptyLakeURI),
		errors.Is(err, ErrNegativeCount),
		errors.Is(err, ErrEmptyKEKHint),
		errors.Is(err, ErrInvalidKeyVersion),
		errors.Is(err, ErrEmptyOrgID),
		errors.Is(err, ErrEmptyJobID),
		errors.Is(err, ErrEmptyPrincipalID),
		errors.Is(err, ErrInvalidPrincipalKind),
		errors.Is(err, ErrInvalidTier),
		errors.Is(err, ErrNegativeMetric):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
