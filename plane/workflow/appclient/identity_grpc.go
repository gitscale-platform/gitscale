package appclient

import (
	"context"
	"fmt"

	identityv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/identity/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

// grpcIdentityClient implements IdentityClient against a generated
// IdentityServiceClient. Per ADR-019, every state-mutating call here is a
// single unary RPC into cmd/identity-service which performs the source write
// + outbox row in one Tx (ADR-008). This adapter holds no state; the caller
// owns the underlying *grpc.ClientConn lifecycle.
type grpcIdentityClient struct {
	c identityv1.IdentityServiceClient
}

// NewGRPCIdentityClient returns an IdentityClient backed by an existing gRPC
// client connection. The connection lifecycle is owned by the caller.
func NewGRPCIdentityClient(cc *grpc.ClientConn) IdentityClient {
	return &grpcIdentityClient{c: identityv1.NewIdentityServiceClient(cc)}
}

func (g *grpcIdentityClient) GetUser(ctx context.Context, id uuid.UUID) (*UserView, error) {
	resp, err := g.c.GetUser(ctx, &identityv1.GetUserRequest{Id: id.String()})
	if err != nil {
		return nil, err
	}
	if !resp.GetFound() {
		return nil, nil
	}
	return userViewFromProto(resp.GetUser())
}

func (g *grpcIdentityClient) GetAgent(ctx context.Context, id uuid.UUID) (*AgentView, error) {
	resp, err := g.c.GetAgent(ctx, &identityv1.GetAgentRequest{Id: id.String()})
	if err != nil {
		return nil, err
	}
	if !resp.GetFound() {
		return nil, nil
	}
	return agentViewFromProto(resp.GetAgent())
}

func (g *grpcIdentityClient) DisableUser(ctx context.Context, userID uuid.UUID, reason string) error {
	_, err := g.c.DisableUser(ctx, &identityv1.DisableUserRequest{UserId: userID.String(), Reason: reason})
	return err
}

func (g *grpcIdentityClient) RevokeAgent(ctx context.Context, agentID uuid.UUID, reason string) error {
	_, err := g.c.RevokeAgent(ctx, &identityv1.RevokeAgentRequest{AgentId: agentID.String(), Reason: reason})
	return err
}

func (g *grpcIdentityClient) UpdateAgentPermissions(ctx context.Context, agentID uuid.UUID, scope []string) error {
	_, err := g.c.UpdateAgentPermissions(ctx, &identityv1.UpdateAgentPermissionsRequest{
		AgentId:         agentID.String(),
		PermissionScope: scope,
	})
	return err
}

func (g *grpcIdentityClient) AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	_, err := g.c.AddOrgMember(ctx, &identityv1.AddOrgMemberRequest{
		OrgId: orgID.String(), UserId: userID.String(), Role: role,
	})
	return err
}

func (g *grpcIdentityClient) RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error {
	_, err := g.c.RemoveOrgMember(ctx, &identityv1.RemoveOrgMemberRequest{
		OrgId: orgID.String(), UserId: userID.String(),
	})
	return err
}

func userViewFromProto(p *identityv1.HumanUser) (*UserView, error) {
	if p == nil {
		return nil, nil
	}
	id, err := uuid.Parse(p.GetId())
	if err != nil {
		return nil, fmt.Errorf("appclient: parse user id: %w", err)
	}
	return &UserView{
		ID:         id,
		Email:      p.GetEmail(),
		RateBucket: p.GetRateBucket(),
		Disabled:   p.GetDisabled(),
	}, nil
}

func agentViewFromProto(p *identityv1.AgentIdentity) (*AgentView, error) {
	if p == nil {
		return nil, nil
	}
	id, err := uuid.Parse(p.GetId())
	if err != nil {
		return nil, fmt.Errorf("appclient: parse agent id: %w", err)
	}
	parent, err := uuid.Parse(p.GetParentUserId())
	if err != nil {
		return nil, fmt.Errorf("appclient: parse parent_user_id: %w", err)
	}
	return &AgentView{
		ID:              id,
		DisplayName:     p.GetDisplayName(),
		ParentUserID:    parent,
		PermissionScope: append([]string{}, p.GetPermissionScope()...),
		RateBucket:      p.GetRateBucket(),
		ReputationScore: p.GetReputationScore(),
		Revoked:         p.GetRevoked(),
	}, nil
}
