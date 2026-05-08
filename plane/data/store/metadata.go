// Package store defines the MetadataStore and Tx interfaces that abstract all
// SQL operations across the five GitScale schema domains. Concrete
// implementations (postgres, stub) satisfy these interfaces; application code
// receives injected instances and never imports a driver directly (ADR-017).
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// MetadataStore is the top-level entry point for all SQL operations.
// Implementations must be safe for concurrent use.
type MetadataStore interface {
	// Transact runs fn inside a serializable transaction. If fn returns a
	// non-nil error the transaction is rolled back; otherwise it is committed.
	// A serialization-failure (SQLSTATE 40001) is surfaced to the caller so
	// retry logic can be applied; use IsRetryable to detect it.
	Transact(ctx context.Context, fn func(Tx) error) error

	// Identity returns a reader for the identity domain. The returned reader
	// is usable outside a transaction for read-only queries.
	Identity() IdentityReader

	// Repositories returns a reader for the repositories domain.
	Repositories() RepositoryReader

	// Billing returns a reader for the billing domain.
	Billing() BillingReader
}

// Tx is a handle for the active transaction passed to Transact callbacks.
// It exposes both read and write operations bounded to the transaction scope.
type Tx interface {
	// Identity returns the identity writer for this transaction.
	Identity() IdentityWriter

	// Repositories returns the repository writer for this transaction.
	Repositories() RepositoryWriter

	// Billing returns the billing writer for this transaction.
	Billing() BillingWriter

	// WriteOutbox appends a row to the domain's outbox table within this
	// transaction. The row is removed if the transaction rolls back.
	// payload is JSON-marshalled; it must be a serialisable value.
	WriteOutbox(
		ctx context.Context,
		domain Domain,
		aggregateType string,
		aggregateID uuid.UUID,
		eventType string,
		payload any,
	) error
}

// HumanUser is the identity.human_users row model.
type HumanUser struct {
	ID             uuid.UUID
	Email          string
	CredentialHash string
	RateBucket     string
	QuotaAccountID *uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AgentIdentity is the identity.agent_identities row model.
type AgentIdentity struct {
	ID               uuid.UUID
	DisplayName      string
	ParentUserID     uuid.UUID
	PermissionScope  []string
	RateBucket       string
	SessionQuota     *int64
	TokensPerWeekCap *int64
	ReputationScore  float64
	QuotaAccountID   *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// OrgMembership is the identity.org_memberships row model.
type OrgMembership struct {
	OrgID  uuid.UUID
	UserID uuid.UUID
	Role   string
}

// IdentityCacheEntry is the minimal projection loaded by the edge-plane
// identity cache for principal resolution.
type IdentityCacheEntry struct {
	PrincipalID uuid.UUID
	Kind        string // "human" or "agent"
	RateBucket  string
	QuotaID     *uuid.UUID
}

// IdentityReader exposes read-only queries against the identity domain.
// Methods defined here may be called both inside and outside a transaction.
type IdentityReader interface {
	GetUserByID(ctx context.Context, id uuid.UUID) (*HumanUser, error)
	GetUserByEmail(ctx context.Context, email string) (*HumanUser, error)
	GetAgentByID(ctx context.Context, id uuid.UUID) (*AgentIdentity, error)
	GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]AgentIdentity, error)
	// LookupIdentityForCache returns the minimal projection used by the edge
	// identity cache. principalID may be a HumanUser or AgentIdentity UUID.
	LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*IdentityCacheEntry, error)
}

// IdentityWriter exposes write operations against the identity domain.
// All methods must be called within a Tx; they panic if used outside one.
type IdentityWriter interface {
	IdentityReader
	InsertHumanUser(ctx context.Context, u HumanUser) error
	InsertAgentIdentity(ctx context.Context, a AgentIdentity) error
	// SetAgentReputationScore sets the score to the given value [0.0, 1.0].
	// Clamping is enforced by the database CHECK constraint; the caller is
	// responsible for computing the new score outside the transaction.
	SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error
	// DisableUser sets human_users.disabled_at = now() and disable_reason = reason.
	// The row is not deleted; downstream consumers gate on disabled_at IS NOT NULL.
	DisableUser(ctx context.Context, userID uuid.UUID, reason string) error
	// RevokeAgent sets agent_identities.revoked_at = now() and revoke_reason = reason.
	RevokeAgent(ctx context.Context, agentID uuid.UUID, reason string) error
	// UpdateAgentPermissions replaces the permission_scope array.
	UpdateAgentPermissions(ctx context.Context, agentID uuid.UUID, scope []string) error
	AddOrgMember(ctx context.Context, m OrgMembership) error
	RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error
}

// Repository is the repositories.repositories row model (minimal projection).
type Repository struct {
	ID            uuid.UUID
	Slug          string
	OrgID         uuid.UUID
	ReplicaSetID  string
	HomeRegion    string
	CreatedAt     time.Time
}

// RepositoryReader exposes read-only queries against the repositories domain.
type RepositoryReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*Repository, error)
	GetBySlug(ctx context.Context, slug string) (*Repository, error)
}

// RepositoryWriter exposes write operations against the repositories domain.
// Not implemented in #14; bodies return errNotImplemented.
type RepositoryWriter interface {
	RepositoryReader
	Insert(ctx context.Context, r Repository) error
	UpdatePermissions(ctx context.Context, repoID uuid.UUID, permissionHash string) error
}

// PartitionArchive is the billing.partition_archives row model.
// It records a successful monthly partition export to the data lake; the
// row + outbox event are written in the same Tx (ADR-008) and idempotency
// is anchored on UNIQUE(year, month, partition_name).
type PartitionArchive struct {
	ID            uuid.UUID
	Year          int
	Month         int
	PartitionName string
	LakeURI       string
	RowCount      int64
	BytesWritten  int64
	ArchivedAt    time.Time
}

// BillingReader exposes read-only queries against the billing domain.
// Methods defined here may be called both inside and outside a transaction.
type BillingReader interface {
	// GetPartitionArchiveByKey returns the row matching the natural key
	// (year, month, partition_name) or (nil, nil) when no row exists.
	GetPartitionArchiveByKey(ctx context.Context, year, month int, partitionName string) (*PartitionArchive, error)

	// ListPartitionArchivesArchivedBefore returns archive rows with
	// archived_at strictly less than cutoff, sorted by (year, month, id)
	// for deterministic workflow iteration. Used by the DEK destruction
	// workflow (#80) to enumerate eligible partitions.
	ListPartitionArchivesArchivedBefore(ctx context.Context, cutoff time.Time) ([]PartitionArchive, error)

	// HasOutboxEventForAggregate returns true when an outbox row already
	// exists for (eventType, aggregateID). Used to anchor idempotency for
	// outbox-only events (e.g. billing.partition_dek_destroyed) that have
	// no source row to UNIQUE-key against.
	HasOutboxEventForAggregate(ctx context.Context, eventType string, aggregateID uuid.UUID) (bool, error)
}

// BillingWriter exposes write operations against the billing domain.
// Methods must be called within a Tx.
type BillingWriter interface {
	BillingReader
	// InsertPartitionArchiveIfAbsent attempts to insert pa. It returns
	// (id, true, nil) if the row was inserted, or (existingID, false, nil)
	// when the natural key already existed (idempotent retry).
	// Errors other than UNIQUE conflict are returned.
	InsertPartitionArchiveIfAbsent(ctx context.Context, pa PartitionArchive) (uuid.UUID, bool, error)
}
