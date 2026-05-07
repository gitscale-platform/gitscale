package appclient

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// IdentityClient is the workflow-plane view of the application-plane
// identity service. Every state-mutating call here corresponds to a
// gRPC unary call into cmd/identity-service that performs a single
// MetadataStore.Transact (writing the source row + the identity_outbox
// row in the same transaction; ADR-008 + ADR-019).
//
// The interface lives here so the workflow plane has a stable contract
// to mock against. The gRPC implementation ships in #15-revocation under
// plane/workflow/appclient/identity_grpc.go.
type IdentityClient interface {
	// GetUser fetches a HumanUser by ID. Returns (nil, nil) if not found.
	GetUser(ctx context.Context, id uuid.UUID) (*UserView, error)
	// GetAgent fetches an AgentIdentity by ID. Returns (nil, nil) if not found.
	GetAgent(ctx context.Context, id uuid.UUID) (*AgentView, error)

	// DisableUser soft-disables a HumanUser. Idempotent on userID.
	// Emits user.disabled to the identity outbox in the same Tx.
	DisableUser(ctx context.Context, userID uuid.UUID, reason string) error

	// RevokeAgent soft-revokes an AgentIdentity. Idempotent on agentID.
	// Emits agent.revoked.
	RevokeAgent(ctx context.Context, agentID uuid.UUID, reason string) error

	// UpdateAgentPermissions replaces the permission_scope array on an
	// AgentIdentity. Emits principal.permissions_changed.
	UpdateAgentPermissions(ctx context.Context, agentID uuid.UUID, scope []string) error

	// AddOrgMember adds a HumanUser to an organisation with a role.
	// Emits org.member_added.
	AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error
	// RemoveOrgMember removes a HumanUser from an organisation. Emits
	// org.member_removed with affected_principal_ids = [userID].
	RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error
}

// UserView is the workflow-plane projection of a HumanUser. Avoids leaking
// the application-layer struct so this package's import graph stays narrow
// (no plane/application/identity from plane/workflow).
type UserView struct {
	ID         uuid.UUID
	Email      string
	RateBucket string
	Disabled   bool
}

// AgentView is the workflow-plane projection of an AgentIdentity.
type AgentView struct {
	ID              uuid.UUID
	DisplayName     string
	ParentUserID    uuid.UUID
	PermissionScope []string
	RateBucket      string
	ReputationScore float64
	Revoked         bool
}

// ErrNotImplemented is returned by the stub implementation of IdentityClient.
// The real gRPC client returned by NewGRPCIdentityClient (in #15-revocation)
// reaches a live cmd/identity-service binary.
var ErrNotImplemented = errors.New("appclient: not implemented in this PR (gRPC impl ships in #15-revocation)")

// stubIdentityClient is the placeholder used by the canary workflow and any
// dependent code that needs an IdentityClient before #15-revocation lands.
// All methods return ErrNotImplemented.
type stubIdentityClient struct{}

// NewStubIdentityClient returns a no-op IdentityClient suitable for tests
// and local boot. Production wiring uses the gRPC client from
// #15-revocation.
func NewStubIdentityClient() IdentityClient { return stubIdentityClient{} }

func (stubIdentityClient) GetUser(_ context.Context, _ uuid.UUID) (*UserView, error) {
	return nil, ErrNotImplemented
}
func (stubIdentityClient) GetAgent(_ context.Context, _ uuid.UUID) (*AgentView, error) {
	return nil, ErrNotImplemented
}
func (stubIdentityClient) DisableUser(_ context.Context, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}
func (stubIdentityClient) RevokeAgent(_ context.Context, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}
func (stubIdentityClient) UpdateAgentPermissions(_ context.Context, _ uuid.UUID, _ []string) error {
	return ErrNotImplemented
}
func (stubIdentityClient) AddOrgMember(_ context.Context, _, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}
func (stubIdentityClient) RemoveOrgMember(_ context.Context, _, _ uuid.UUID) error {
	return ErrNotImplemented
}
