package identity

import (
	"context"
	"errors"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/google/uuid"
)

// Service is the identity domain service. Methods that emit events do so via
// the outbox in the same Tx as the source write (ADR-008). State mutations
// are exclusive to this service in the application plane; the workflow plane
// reaches it through the application-plane gRPC surface (ADR-019).
type Service interface {
	// Reads
	GetUser(ctx context.Context, id uuid.UUID) (*HumanUser, error)
	GetUserByEmail(ctx context.Context, email string) (*HumanUser, error)
	GetAgent(ctx context.Context, id uuid.UUID) (*AgentIdentity, error)
	GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]AgentIdentity, error)
	LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*cache.IdentityCacheEntry, error)

	// Creates (#15-stub + #15-postgres)
	CreateUser(ctx context.Context, email, plaintextCredential string) (*HumanUser, error)
	CreateAgent(ctx context.Context, parentUserID uuid.UUID, displayName string, scope []string) (*AgentIdentity, error)

	// Reputation (#15-postgres in production; stub also implements)
	SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error

	// Revocation surface (#15-revocation; stub returns ErrNotImplemented)
	DisableUser(ctx context.Context, id uuid.UUID, reason string) error
	RevokeAgent(ctx context.Context, id uuid.UUID, reason string) error
	UpdateAgentPermissions(ctx context.Context, id uuid.UUID, scope []string) error

	// Org membership (#15-revocation; stub returns ErrNotImplemented)
	AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error
	RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error
}

// Service-level sentinel errors.
var (
	// ErrNotImplemented is returned by methods not implemented in this PR.
	// The trailing "(#15-revocation)" hint helps callers locate the impl.
	ErrNotImplemented = errors.New("identity: not implemented in this PR (available in #15-revocation)")

	// ErrRetryExhausted is returned when WithSerializableRetry has consumed
	// its budget without observing a non-retryable outcome.
	ErrRetryExhausted = errors.New("identity: serializable retry exhausted")

	// ErrInvalidEmail is returned by CreateUser for an obviously-malformed
	// email value. Full RFC validation is out of scope.
	ErrInvalidEmail = errors.New("identity: invalid email")

	// ErrEmptyDisplayName is returned by CreateAgent when display name is empty.
	ErrEmptyDisplayName = errors.New("identity: agent display name is empty")

	// ErrAgentNotFound is returned by SetAgentReputationScore / RevokeAgent /
	// UpdateAgentPermissions when the target agent does not exist.
	ErrAgentNotFound = errors.New("identity: agent not found")

	// ErrUserNotFound is returned by DisableUser / AddOrgMember / RemoveOrgMember
	// when the target user does not exist.
	ErrUserNotFound = errors.New("identity: user not found")

	// ErrEmptyRole is returned by AddOrgMember when role is empty.
	ErrEmptyRole = errors.New("identity: org membership role is empty")
)
