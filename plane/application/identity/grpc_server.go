package identity

import (
	"context"
	"errors"

	identityv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/identity/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer adapts the in-process Service to the generated IdentityService
// gRPC surface. It owns no state beyond the wrapped Service; callers are
// responsible for transactional integrity (the underlying Service ensures
// source row + outbox row land in the same Tx per ADR-008).
type GRPCServer struct {
	identityv1.UnimplementedIdentityServiceServer
	svc Service
}

// NewGRPCServer wraps svc.
func NewGRPCServer(svc Service) *GRPCServer { return &GRPCServer{svc: svc} }

func (s *GRPCServer) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	u, err := s.svc.GetUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	if u == nil {
		return &identityv1.GetUserResponse{Found: false}, nil
	}
	return &identityv1.GetUserResponse{Found: true, User: userToProto(u)}, nil
}

func (s *GRPCServer) GetAgent(ctx context.Context, req *identityv1.GetAgentRequest) (*identityv1.GetAgentResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "id: %v", err)
	}
	a, err := s.svc.GetAgent(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	if a == nil {
		return &identityv1.GetAgentResponse{Found: false}, nil
	}
	return &identityv1.GetAgentResponse{Found: true, Agent: agentToProto(a)}, nil
}

func (s *GRPCServer) CreateUser(ctx context.Context, req *identityv1.CreateUserRequest) (*identityv1.CreateUserResponse, error) {
	u, err := s.svc.CreateUser(ctx, req.GetEmail(), req.GetPlaintextCredential())
	if err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.CreateUserResponse{User: userToProto(u)}, nil
}

func (s *GRPCServer) CreateAgent(ctx context.Context, req *identityv1.CreateAgentRequest) (*identityv1.CreateAgentResponse, error) {
	parent, err := uuid.Parse(req.GetParentUserId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "parent_user_id: %v", err)
	}
	a, err := s.svc.CreateAgent(ctx, parent, req.GetDisplayName(), req.GetPermissionScope())
	if err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.CreateAgentResponse{Agent: agentToProto(a)}, nil
}

func (s *GRPCServer) DisableUser(ctx context.Context, req *identityv1.DisableUserRequest) (*identityv1.DisableUserResponse, error) {
	id, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "user_id: %v", err)
	}
	if err := s.svc.DisableUser(ctx, id, req.GetReason()); err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.DisableUserResponse{}, nil
}

func (s *GRPCServer) RevokeAgent(ctx context.Context, req *identityv1.RevokeAgentRequest) (*identityv1.RevokeAgentResponse, error) {
	id, err := uuid.Parse(req.GetAgentId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "agent_id: %v", err)
	}
	if err := s.svc.RevokeAgent(ctx, id, req.GetReason()); err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.RevokeAgentResponse{}, nil
}

func (s *GRPCServer) UpdateAgentPermissions(ctx context.Context, req *identityv1.UpdateAgentPermissionsRequest) (*identityv1.UpdateAgentPermissionsResponse, error) {
	id, err := uuid.Parse(req.GetAgentId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "agent_id: %v", err)
	}
	if err := s.svc.UpdateAgentPermissions(ctx, id, req.GetPermissionScope()); err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.UpdateAgentPermissionsResponse{}, nil
}

func (s *GRPCServer) AddOrgMember(ctx context.Context, req *identityv1.AddOrgMemberRequest) (*identityv1.AddOrgMemberResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "org_id: %v", err)
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "user_id: %v", err)
	}
	if err := s.svc.AddOrgMember(ctx, orgID, userID, req.GetRole()); err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.AddOrgMemberResponse{}, nil
}

func (s *GRPCServer) RemoveOrgMember(ctx context.Context, req *identityv1.RemoveOrgMemberRequest) (*identityv1.RemoveOrgMemberResponse, error) {
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "org_id: %v", err)
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "user_id: %v", err)
	}
	if err := s.svc.RemoveOrgMember(ctx, orgID, userID); err != nil {
		return nil, mapErr(err)
	}
	return &identityv1.RemoveOrgMemberResponse{}, nil
}

func userToProto(u *HumanUser) *identityv1.HumanUser {
	out := &identityv1.HumanUser{
		Id:         u.ID.String(),
		Email:      u.Email,
		RateBucket: u.RateBucket,
	}
	if u.QuotaAccountID != nil {
		out.QuotaAccountId = u.QuotaAccountID.String()
	}
	return out
}

func agentToProto(a *AgentIdentity) *identityv1.AgentIdentity {
	out := &identityv1.AgentIdentity{
		Id:              a.ID.String(),
		DisplayName:     a.DisplayName,
		ParentUserId:    a.ParentUserID.String(),
		PermissionScope: append([]string{}, a.PermissionScope...),
		RateBucket:      a.RateBucket,
		ReputationScore: a.ReputationScore,
	}
	if a.QuotaAccountID != nil {
		out.QuotaAccountId = a.QuotaAccountID.String()
	}
	return out
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrInvalidEmail), errors.Is(err, ErrEmptyDisplayName), errors.Is(err, ErrEmptyRole):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrUserNotFound), errors.Is(err, ErrAgentNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrNotImplemented):
		return status.Error(codes.Unimplemented, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
