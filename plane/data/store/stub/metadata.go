// Package stub provides an in-memory MetadataStore implementation for use in
// unit tests. It records all writes and makes them available via Recorded().
// Rollback semantics are simulated: ops recorded during a failed transaction
// are discarded.
package stub

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// OutboxRecord captures one WriteOutbox call for test assertions.
type OutboxRecord struct {
	Domain        store.Domain
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Payload       any
	EventID       uuid.UUID
}

// Store is the in-memory implementation of store.MetadataStore.
type Store struct {
	mu                sync.Mutex
	users             map[uuid.UUID]*store.HumanUser
	agents            map[uuid.UUID]*store.AgentIdentity
	repositories      map[uuid.UUID]*store.Repository
	partitionArchives map[string]store.PartitionArchive
	cloneTokens       map[uuid.UUID]*store.CloneToken
	outbox            []OutboxRecord
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		users:             make(map[uuid.UUID]*store.HumanUser),
		agents:            make(map[uuid.UUID]*store.AgentIdentity),
		repositories:      make(map[uuid.UUID]*store.Repository),
		partitionArchives: make(map[string]store.PartitionArchive),
		cloneTokens:       make(map[uuid.UUID]*store.CloneToken),
	}
}

// CloneTokens returns a snapshot of recorded clone-token rows. Used by
// tests that need to assert MintCloneToken's storage side-effect.
func (s *Store) CloneTokens() []store.CloneToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.CloneToken, 0, len(s.cloneTokens))
	for _, ct := range s.cloneTokens {
		out = append(out, *ct)
	}
	return out
}

// Recorded returns all committed OutboxRecords in insertion order.
func (s *Store) Recorded() []OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OutboxRecord, len(s.outbox))
	copy(out, s.outbox)
	return out
}

// Transact runs fn with a transaction handle. On nil return the writes are
// committed; on error they are discarded.
func (s *Store) Transact(_ context.Context, fn func(store.Tx) error) error {
	tx := &stubTx{store: s}
	if err := fn(tx); err != nil {
		return err
	}
	// Commit: apply pending writes.
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, u := range tx.pendingUsers {
		s.users[id] = u
	}
	for id, a := range tx.pendingAgents {
		s.agents[id] = a
	}
	for id, r := range tx.pendingRepositories {
		s.repositories[id] = r
	}
	for id, ct := range tx.pendingCloneTokens {
		s.cloneTokens[id] = ct
	}
	for k, pa := range tx.pendingPartitionArchives {
		// First-writer-wins on commit: do not overwrite an existing row.
		// The Tx.Billing() writer already returns the existing id on
		// duplicate, so this guard mirrors postgres ON CONFLICT DO NOTHING.
		if _, exists := s.partitionArchives[k]; !exists {
			s.partitionArchives[k] = pa
		}
	}
	s.outbox = append(s.outbox, tx.pendingOutbox...)
	return nil
}

// Identity returns a reader backed by the in-memory store.
func (s *Store) Identity() store.IdentityReader {
	return &stubIdentityReader{store: s}
}

// Repositories returns a stub repository reader.
func (s *Store) Repositories() store.RepositoryReader {
	return &stubRepositoryReader{store: s}
}

// stubTx is the transaction handle passed to Transact callbacks.
type stubTx struct {
	store                    *Store
	pendingUsers             map[uuid.UUID]*store.HumanUser
	pendingAgents            map[uuid.UUID]*store.AgentIdentity
	pendingRepositories      map[uuid.UUID]*store.Repository
	pendingPartitionArchives map[string]store.PartitionArchive
	pendingCloneTokens       map[uuid.UUID]*store.CloneToken
	pendingOutbox            []OutboxRecord
}

func (t *stubTx) lazyInit() {
	if t.pendingUsers == nil {
		t.pendingUsers = make(map[uuid.UUID]*store.HumanUser)
	}
	if t.pendingAgents == nil {
		t.pendingAgents = make(map[uuid.UUID]*store.AgentIdentity)
	}
	if t.pendingRepositories == nil {
		t.pendingRepositories = make(map[uuid.UUID]*store.Repository)
	}
}

func (t *stubTx) Identity() store.IdentityWriter {
	return &stubIdentityWriter{tx: t, reader: &stubIdentityReader{store: t.store}}
}

func (t *stubTx) Repositories() store.RepositoryWriter {
	return &stubRepositoryWriter{tx: t, reader: &stubRepositoryReader{store: t.store}}
}

func (t *stubTx) WriteOutbox(
	_ context.Context,
	domain store.Domain,
	aggregateType string,
	aggregateID uuid.UUID,
	eventType string,
	payload any,
) error {
	if !domain.Valid() {
		return errors.New("stub: WriteOutbox: invalid domain")
	}
	t.pendingOutbox = append(t.pendingOutbox, OutboxRecord{
		Domain:        domain,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       payload,
		EventID:       store.NewEventID(),
	})
	return nil
}

// stubIdentityReader reads from the committed store state.
type stubIdentityReader struct {
	store *Store
}

func (r *stubIdentityReader) GetUserByID(_ context.Context, id uuid.UUID) (*store.HumanUser, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	u := r.store.users[id]
	if u == nil {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

func (r *stubIdentityReader) GetUserByEmail(_ context.Context, email string) (*store.HumanUser, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, u := range r.store.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *stubIdentityReader) GetAgentByID(_ context.Context, id uuid.UUID) (*store.AgentIdentity, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	a := r.store.agents[id]
	if a == nil {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (r *stubIdentityReader) GetAgentsByParentUser(_ context.Context, userID uuid.UUID) ([]store.AgentIdentity, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	var agents []store.AgentIdentity
	for _, a := range r.store.agents {
		if a.ParentUserID == userID {
			agents = append(agents, *a)
		}
	}
	return agents, nil
}

func (r *stubIdentityReader) LookupIdentityForCache(_ context.Context, principalID uuid.UUID) (*store.IdentityCacheEntry, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if u, ok := r.store.users[principalID]; ok {
		return &store.IdentityCacheEntry{
			PrincipalID: u.ID,
			Kind:        "human",
			RateBucket:  u.RateBucket,
			QuotaID:     u.QuotaAccountID,
		}, nil
	}
	if a, ok := r.store.agents[principalID]; ok {
		return &store.IdentityCacheEntry{
			PrincipalID: a.ID,
			Kind:        "agent",
			RateBucket:  a.RateBucket,
			QuotaID:     a.QuotaAccountID,
		}, nil
	}
	return nil, nil
}

// stubIdentityWriter combines committed reads with pending writes.
type stubIdentityWriter struct {
	tx     *stubTx
	reader *stubIdentityReader
}

func (w *stubIdentityWriter) GetUserByID(ctx context.Context, id uuid.UUID) (*store.HumanUser, error) {
	w.tx.lazyInit()
	if u, ok := w.tx.pendingUsers[id]; ok {
		cp := *u
		return &cp, nil
	}
	return w.reader.GetUserByID(ctx, id)
}

func (w *stubIdentityWriter) GetUserByEmail(ctx context.Context, email string) (*store.HumanUser, error) {
	w.tx.lazyInit()
	for _, u := range w.tx.pendingUsers {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return w.reader.GetUserByEmail(ctx, email)
}

func (w *stubIdentityWriter) GetAgentByID(ctx context.Context, id uuid.UUID) (*store.AgentIdentity, error) {
	w.tx.lazyInit()
	if a, ok := w.tx.pendingAgents[id]; ok {
		cp := *a
		return &cp, nil
	}
	return w.reader.GetAgentByID(ctx, id)
}

func (w *stubIdentityWriter) GetAgentsByParentUser(ctx context.Context, userID uuid.UUID) ([]store.AgentIdentity, error) {
	return w.reader.GetAgentsByParentUser(ctx, userID)
}

func (w *stubIdentityWriter) LookupIdentityForCache(ctx context.Context, principalID uuid.UUID) (*store.IdentityCacheEntry, error) {
	return w.reader.LookupIdentityForCache(ctx, principalID)
}

func (w *stubIdentityWriter) InsertHumanUser(_ context.Context, u store.HumanUser) error {
	w.tx.lazyInit()
	cp := u
	w.tx.pendingUsers[u.ID] = &cp
	return nil
}

func (w *stubIdentityWriter) InsertAgentIdentity(_ context.Context, a store.AgentIdentity) error {
	w.tx.lazyInit()
	cp := a
	w.tx.pendingAgents[a.ID] = &cp
	return nil
}

func (w *stubIdentityWriter) SetAgentReputationScore(_ context.Context, agentID uuid.UUID, score float64) error {
	w.tx.lazyInit()
	if a, ok := w.tx.pendingAgents[agentID]; ok {
		a.ReputationScore = score
		return nil
	}
	w.reader.store.mu.Lock()
	existing, ok := w.reader.store.agents[agentID]
	w.reader.store.mu.Unlock()
	if !ok {
		return errors.New("stub: SetAgentReputationScore: agent not found")
	}
	cp := *existing
	cp.ReputationScore = score
	w.tx.pendingAgents[agentID] = &cp
	return nil
}

// DisableUser is a no-op on the stub model: HumanUser does not carry a
// disabled_at field so the in-memory projection is unchanged. The stub still
// participates in the outbox path so service-level event tests pass.
func (w *stubIdentityWriter) DisableUser(_ context.Context, userID uuid.UUID, _ string) error {
	w.tx.lazyInit()
	w.reader.store.mu.Lock()
	_, ok := w.reader.store.users[userID]
	w.reader.store.mu.Unlock()
	if !ok {
		if _, pending := w.tx.pendingUsers[userID]; !pending {
			return errors.New("stub: DisableUser: user not found")
		}
	}
	return nil
}

func (w *stubIdentityWriter) RevokeAgent(_ context.Context, agentID uuid.UUID, _ string) error {
	w.tx.lazyInit()
	w.reader.store.mu.Lock()
	_, ok := w.reader.store.agents[agentID]
	w.reader.store.mu.Unlock()
	if !ok {
		if _, pending := w.tx.pendingAgents[agentID]; !pending {
			return errors.New("stub: RevokeAgent: agent not found")
		}
	}
	return nil
}

func (w *stubIdentityWriter) UpdateAgentPermissions(_ context.Context, agentID uuid.UUID, scope []string) error {
	w.tx.lazyInit()
	w.reader.store.mu.Lock()
	existing, ok := w.reader.store.agents[agentID]
	w.reader.store.mu.Unlock()
	if !ok {
		if pending, p := w.tx.pendingAgents[agentID]; p {
			cp := *pending
			cp.PermissionScope = append([]string{}, scope...)
			w.tx.pendingAgents[agentID] = &cp
			return nil
		}
		return errors.New("stub: UpdateAgentPermissions: agent not found")
	}
	cp := *existing
	cp.PermissionScope = append([]string{}, scope...)
	w.tx.pendingAgents[agentID] = &cp
	return nil
}

func (w *stubIdentityWriter) AddOrgMember(_ context.Context, _ store.OrgMembership) error {
	return nil
}

func (w *stubIdentityWriter) RemoveOrgMember(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (w *stubIdentityWriter) InsertCloneToken(_ context.Context, ct store.CloneToken) error {
	w.tx.lazyInit()
	if w.tx.pendingCloneTokens == nil {
		w.tx.pendingCloneTokens = make(map[uuid.UUID]*store.CloneToken)
	}
	cp := ct
	w.tx.pendingCloneTokens[ct.ID] = &cp
	return nil
}

type stubRepositoryReader struct {
	store *Store
}

func (r *stubRepositoryReader) GetByID(_ context.Context, id uuid.UUID) (*store.Repository, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	rep := r.store.repositories[id]
	if rep == nil {
		return nil, nil
	}
	cp := *rep
	return &cp, nil
}

func (r *stubRepositoryReader) GetBySlug(_ context.Context, slug string) (*store.Repository, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, rep := range r.store.repositories {
		if rep.Slug == slug {
			cp := *rep
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *stubRepositoryReader) ListByOrg(
	_ context.Context,
	orgID uuid.UUID,
	afterCreatedAt *time.Time,
	afterID *uuid.UUID,
	limit int,
) ([]store.Repository, error) {
	if limit <= 0 {
		return nil, nil
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	var all []store.Repository
	for _, rep := range r.store.repositories {
		if rep.OrgID != orgID {
			continue
		}
		all = append(all, *rep)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID.String() < all[j].ID.String()
	})

	out := make([]store.Repository, 0, limit)
	for _, rep := range all {
		if afterCreatedAt != nil && afterID != nil {
			if rep.CreatedAt.Before(*afterCreatedAt) {
				continue
			}
			if rep.CreatedAt.Equal(*afterCreatedAt) && rep.ID.String() <= afterID.String() {
				continue
			}
		}
		out = append(out, rep)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

type stubRepositoryWriter struct {
	tx     *stubTx
	reader *stubRepositoryReader
}

func (w *stubRepositoryWriter) GetByID(ctx context.Context, id uuid.UUID) (*store.Repository, error) {
	w.tx.lazyInit()
	if r, ok := w.tx.pendingRepositories[id]; ok {
		cp := *r
		return &cp, nil
	}
	return w.reader.GetByID(ctx, id)
}

func (w *stubRepositoryWriter) GetBySlug(ctx context.Context, slug string) (*store.Repository, error) {
	w.tx.lazyInit()
	for _, r := range w.tx.pendingRepositories {
		if r.Slug == slug {
			cp := *r
			return &cp, nil
		}
	}
	return w.reader.GetBySlug(ctx, slug)
}

func (w *stubRepositoryWriter) ListByOrg(
	ctx context.Context,
	orgID uuid.UUID,
	afterCreatedAt *time.Time,
	afterID *uuid.UUID,
	limit int,
) ([]store.Repository, error) {
	return w.reader.ListByOrg(ctx, orgID, afterCreatedAt, afterID, limit)
}

func (w *stubRepositoryWriter) Insert(_ context.Context, r store.Repository) error {
	w.tx.lazyInit()
	cp := r
	w.tx.pendingRepositories[r.ID] = &cp
	return nil
}

func (w *stubRepositoryWriter) UpdatePermissions(_ context.Context, _ uuid.UUID, _ string) error {
	return errors.New("stub: UpdatePermissions not implemented")
}
