package identity

import (
	"context"
	"errors"
	"time"

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

	// Clone-token mint (#112 MCP server). Returns a short-lived
	// (CloneTokenTTL) opaque token scoped to the (principal, repo) pair.
	// Both stub and postgres impls write the source row + a
	// `clone_token_minted` outbox row in the same Tx (ADR-008). Audit /
	// revocation consumers attach to the outbox event.
	MintCloneToken(ctx context.Context, principalID, repoID uuid.UUID) (CloneToken, error)
}

// CloneToken is the result of Service.MintCloneToken. The token is opaque
// to callers; storage maps it back to (principal_id, repo_id, expires_at).
// CloneURL is filled in by the caller (or remains empty when the identity
// service has no opinion on URL shape — the MCP layer constructs the
// final clone URL from configuration).
type CloneToken struct {
	// TokenID is the storage row's primary key, usable as an audit handle.
	TokenID uuid.UUID
	// Token is the opaque secret returned to the agent. Treated as a
	// bearer credential; never logged.
	Token string
	// PrincipalID is the principal the token was minted for.
	PrincipalID uuid.UUID
	// RepoID is the repository the token grants clone access to.
	RepoID uuid.UUID
	// ExpiresAt is the wall-clock TTL boundary.
	ExpiresAt time.Time
}

// CloneTokenTTL is the lifetime of a clone token minted via
// Service.MintCloneToken. Short enough to bound blast radius if the
// token is exfiltrated; long enough that an interactive agent clone
// (multi-GB repos) finishes inside one TTL.
const CloneTokenTTL = 15 * time.Minute

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
