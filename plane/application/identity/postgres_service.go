package identity

import (
	"context"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// postgresService implements Service against any store.MetadataStore. It is
// not coupled to the postgres concrete type — the constructor receives the
// interface so cmd/identity-service (lands in #15-revocation) can inject a
// pgxpool-backed instance and tests can inject the stub.
//
// State mutations open exactly one Transact and emit the source row + outbox
// row in the same Tx (ADR-008). Serialization-failure (40001) retries are
// bounded by WithSerializableRetry; non-retryable errors propagate.
type postgresService struct {
	store  store.MetadataStore
	hasher CredentialHasher
	clock  func() time.Time
}

// NewPostgresService returns a Service backed by ms and using the production
// Argon2id hasher. Callers in tests should use newPostgresServiceWithHasher
// to override the hasher with cheaper parameters.
func NewPostgresService(ms store.MetadataStore) Service {
	return newPostgresServiceWithHasher(ms, NewArgon2idHasher())
}

func newPostgresServiceWithHasher(ms store.MetadataStore, h CredentialHasher) *postgresService {
	return &postgresService{store: ms, hasher: h, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *postgresService) GetUser(ctx context.Context, id uuid.UUID) (*HumanUser, error) {
	return s.store.Identity().GetUserByID(ctx, id)
}

func (s *postgresService) GetUserByEmail(ctx context.Context, email string) (*HumanUser, error) {
	return s.store.Identity().GetUserByEmail(ctx, normalizeEmail(email))
}

func (s *postgresService) GetAgent(ctx context.Context, id uuid.UUID) (*AgentIdentity, error) {
	return s.store.Identity().GetAgentByID(ctx, id)
}

func (s *postgresService) GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]AgentIdentity, error) {
	return s.store.Identity().GetAgentsByParentUser(ctx, userID)
}

func (s *postgresService) LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*cache.IdentityCacheEntry, error) {
	entry, err := s.store.Identity().LookupIdentityForCache(ctx, principalID)
	if err != nil || entry == nil {
		return nil, err
	}
	return &cache.IdentityCacheEntry{
		Version:     1,
		PrincipalID: entry.PrincipalID.String(),
	}, nil
}

func (s *postgresService) CreateUser(ctx context.Context, email, plaintextCredential string) (*HumanUser, error) {
	if !looksLikeEmail(email) {
		return nil, ErrInvalidEmail
	}
	if plaintextCredential == "" {
		return nil, ErrCredentialEmpty
	}

	hashed, err := s.hasher.Hash(plaintextCredential)
	if err != nil {
		return nil, err
	}

	user := HumanUser{
		ID:             uuid.New(),
		Email:          normalizeEmail(email),
		CredentialHash: hashed,
		RateBucket:     "human_default",
		CreatedAt:      s.clock(),
		UpdatedAt:      s.clock(),
	}

	err = WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			if err := tx.Identity().InsertHumanUser(ctx, user); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", user.ID, EventUserCreated, newUserCreatedPayload(user))
		})
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *postgresService) CreateAgent(ctx context.Context, parentUserID uuid.UUID, displayName string, scope []string) (*AgentIdentity, error) {
	if displayName == "" {
		return nil, ErrEmptyDisplayName
	}

	agent := AgentIdentity{
		ID:              uuid.New(),
		DisplayName:     displayName,
		ParentUserID:    parentUserID,
		PermissionScope: append([]string{}, scope...),
		RateBucket:      "agent_standard",
		ReputationScore: 0.5,
		CreatedAt:       s.clock(),
		UpdatedAt:       s.clock(),
	}

	err := WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			if err := tx.Identity().InsertAgentIdentity(ctx, agent); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "agent_identity", agent.ID, EventAgentCreated, newAgentCreatedPayload(agent))
		})
	})
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// SetAgentReputationScore reads the current score, writes the new (clamped)
// value, and emits agent.reputation_updated with the delta — all in one Tx.
// Concurrent updates surface as 40001 to the retry helper, which restarts
// the read-modify-write cycle.
func (s *postgresService) SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error {
	clamped := clamp01(score)
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			existing, err := tx.Identity().GetAgentByID(ctx, agentID)
			if err != nil {
				return err
			}
			if existing == nil {
				return ErrAgentNotFound
			}
			oldScore := existing.ReputationScore
			if err := tx.Identity().SetAgentReputationScore(ctx, agentID, clamped); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "agent_identity", agentID, EventAgentReputationUpdated,
				newAgentReputationUpdatedPayload(agentID, oldScore, clamped))
		})
	})
}

func (s *postgresService) DisableUser(ctx context.Context, id uuid.UUID, reason string) error {
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			existing, err := tx.Identity().GetUserByID(ctx, id)
			if err != nil {
				return err
			}
			if existing == nil {
				return ErrUserNotFound
			}
			if err := tx.Identity().DisableUser(ctx, id, reason); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", id,
				EventUserDisabled, newUserDisabledPayload(id, reason, s.clock()))
		})
	})
}

func (s *postgresService) RevokeAgent(ctx context.Context, id uuid.UUID, reason string) error {
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			existing, err := tx.Identity().GetAgentByID(ctx, id)
			if err != nil {
				return err
			}
			if existing == nil {
				return ErrAgentNotFound
			}
			if err := tx.Identity().RevokeAgent(ctx, id, reason); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "agent_identity", id,
				EventAgentRevoked, newAgentRevokedPayload(id, existing.ParentUserID, reason, s.clock()))
		})
	})
}

func (s *postgresService) UpdateAgentPermissions(ctx context.Context, id uuid.UUID, scope []string) error {
	newScope := append([]string{}, scope...)
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			existing, err := tx.Identity().GetAgentByID(ctx, id)
			if err != nil {
				return err
			}
			if existing == nil {
				return ErrAgentNotFound
			}
			oldScope := append([]string{}, existing.PermissionScope...)
			if err := tx.Identity().UpdateAgentPermissions(ctx, id, newScope); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "agent_identity", id,
				EventPrincipalPermissionsChanged,
				newPrincipalPermissionsChangedPayload(id, oldScope, newScope, s.clock()))
		})
	})
}

func (s *postgresService) AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	if role == "" {
		return ErrEmptyRole
	}
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			user, err := tx.Identity().GetUserByID(ctx, userID)
			if err != nil {
				return err
			}
			if user == nil {
				return ErrUserNotFound
			}
			if err := tx.Identity().AddOrgMember(ctx, store.OrgMembership{
				OrgID: orgID, UserID: userID, Role: role,
			}); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "org_membership", userID,
				EventOrgMemberAdded, newOrgMemberAddedPayload(orgID, userID, role, s.clock()))
		})
	})
}

func (s *postgresService) MintCloneToken(ctx context.Context, principalID, repoID uuid.UUID) (CloneToken, error) {
	secret, err := generateCloneTokenSecret()
	if err != nil {
		return CloneToken{}, err
	}
	var out CloneToken
	err = WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			minted, err := mintCloneTokenInTx(ctx, tx, s.clock(), principalID, repoID, secret)
			if err != nil {
				return err
			}
			out = minted
			return nil
		})
	})
	if err != nil {
		return CloneToken{}, err
	}
	return out, nil
}

func (s *postgresService) RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error {
	return WithSerializableRetry(ctx, func() error {
		return s.store.Transact(ctx, func(tx store.Tx) error {
			if err := tx.Identity().RemoveOrgMember(ctx, orgID, userID); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "org_membership", userID,
				EventOrgMemberRemoved, newOrgMemberRemovedPayload(orgID, userID, s.clock()))
		})
	})
}
