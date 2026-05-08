package billing

import (
	"context"
	"errors"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
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

// mapErr converts a billing domain error into a gRPC status. Validation
// failures map to InvalidArgument; unknown errors are reported as Internal.
func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrInvalidYear),
		errors.Is(err, ErrInvalidMonth),
		errors.Is(err, ErrEmptyPartitionName),
		errors.Is(err, ErrEmptyLakeURI),
		errors.Is(err, ErrNegativeCount),
		errors.Is(err, ErrEmptyKEKHint),
		errors.Is(err, ErrInvalidKeyVersion):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
