package identity

import (
	"context"
	"strings"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

// stubService implements Service over an in-memory stub.Store. Production
// code uses a postgres-backed implementation; this exists so service-level
// behaviour (event payload shapes, transactional boundaries, retry semantics)
// can be exercised in unit tests without a real database.
type stubService struct {
	store  *stub.Store
	hasher CredentialHasher
	clock  func() time.Time
}

// NewStubService returns a Service backed by a fresh in-memory store and the
// production-tuned argon2id hasher. Callers may swap the hasher via the
// internal constructor if test runtime is a concern.
func NewStubService() Service {
	return newStubServiceWithHasher(stub.New(), NewArgon2idHasher())
}

func newStubServiceWithHasher(s *stub.Store, h CredentialHasher) *stubService {
	return &stubService{store: s, hasher: h, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *stubService) GetUser(ctx context.Context, id uuid.UUID) (*HumanUser, error) {
	return s.store.Identity().GetUserByID(ctx, id)
}

func (s *stubService) GetUserByEmail(ctx context.Context, email string) (*HumanUser, error) {
	return s.store.Identity().GetUserByEmail(ctx, normalizeEmail(email))
}

func (s *stubService) GetAgent(ctx context.Context, id uuid.UUID) (*AgentIdentity, error) {
	return s.store.Identity().GetAgentByID(ctx, id)
}

func (s *stubService) GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]AgentIdentity, error) {
	return s.store.Identity().GetAgentsByParentUser(ctx, userID)
}

// LookupIdentityForCache adapts the storage-layer projection to the
// cache-layer entry shape consumed by the edge plane (ADR-009).
func (s *stubService) LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*cache.IdentityCacheEntry, error) {
	entry, err := s.store.Identity().LookupIdentityForCache(ctx, principalID)
	if err != nil || entry == nil {
		return nil, err
	}
	return &cache.IdentityCacheEntry{
		Version:     1,
		PrincipalID: entry.PrincipalID.String(),
		// OrgID and Roles require additional lookups; populated by the
		// postgres impl in #15-postgres. Stub returns the minimal projection.
	}, nil
}

func (s *stubService) CreateUser(ctx context.Context, email, plaintextCredential string) (*HumanUser, error) {
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

func (s *stubService) CreateAgent(ctx context.Context, parentUserID uuid.UUID, displayName string, scope []string) (*AgentIdentity, error) {
	if displayName == "" {
		return nil, ErrEmptyDisplayName
	}
	agent := AgentIdentity{
		ID:              uuid.New(),
		DisplayName:     displayName,
		ParentUserID:    parentUserID,
		PermissionScope: append([]string(nil), scope...),
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

func (s *stubService) SetAgentReputationScore(ctx context.Context, agentID uuid.UUID, score float64) error {
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

func (s *stubService) DisableUser(_ context.Context, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}

func (s *stubService) RevokeAgent(_ context.Context, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}

func (s *stubService) UpdateAgentPermissions(_ context.Context, _ uuid.UUID, _ []string) error {
	return ErrNotImplemented
}

func (s *stubService) AddOrgMember(_ context.Context, _, _ uuid.UUID, _ string) error {
	return ErrNotImplemented
}

func (s *stubService) RemoveOrgMember(_ context.Context, _, _ uuid.UUID) error {
	return ErrNotImplemented
}

// normalizeEmail lowercases + trims whitespace so reads via GetUserByEmail
// match regardless of caller capitalisation. The DB UNIQUE constraint sees
// normalized values only.
func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.IndexByte(s[at+1:], '.') < 0 {
		return false
	}
	return true
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
