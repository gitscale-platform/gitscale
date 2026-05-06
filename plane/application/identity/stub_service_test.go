package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

func newTestService(t *testing.T) (*stubService, *stub.Store) {
	t.Helper()
	s := stub.New()
	return newStubServiceWithHasher(s, newArgon2idHasherFast()), s
}

func TestCreateUser_emitsUserCreated_inSameTx(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()

	u, err := svc.CreateUser(ctx, "Alice@Example.COM", "hunter2")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u == nil || u.ID == uuid.Nil {
		t.Fatal("expected non-nil user with ID")
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email not normalized: got %q", u.Email)
	}

	// Source row exists.
	got, err := svc.GetUser(ctx, u.ID)
	if err != nil || got == nil {
		t.Fatalf("GetUser: %v / %v", got, err)
	}

	// Outbox row recorded with EventUserCreated.
	rec := store_.Recorded()
	if len(rec) != 1 {
		t.Fatalf("expected 1 outbox record, got %d", len(rec))
	}
	if rec[0].EventType != EventUserCreated || rec[0].Domain != store.DomainIdentity {
		t.Errorf("wrong outbox metadata: %+v", rec[0])
	}
	if rec[0].AggregateID != u.ID {
		t.Errorf("aggregate_id mismatch: %s vs %s", rec[0].AggregateID, u.ID)
	}
}

func TestCreateUser_rollbackOnInvalidEmail_recordsNothing(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, "no-at-sign", "x"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
	if len(store_.Recorded()) != 0 {
		t.Fatalf("expected no outbox writes for rejected input, got %d", len(store_.Recorded()))
	}
}

func TestCreateUser_payloadCarriesMeteringFields(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, "metric@test.dev", "pw"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	rec := store_.Recorded()[0]
	raw, _ := json.Marshal(rec.Payload)
	var p UserCreatedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.RateBucket == "" {
		t.Error("rate_bucket missing from user.created payload")
	}
	if p.PrincipalClass != "user" {
		t.Errorf("principal_class: got %q want \"user\"", p.PrincipalClass)
	}
	if p.EnvelopeVersion != 1 {
		t.Errorf("_envelope_version: got %d want 1", p.EnvelopeVersion)
	}
}

func TestCreateAgent_emitsAgentCreated_withMeteringFields(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()
	parent := uuid.New()

	a, err := svc.CreateAgent(ctx, parent, "code-reviewer", []string{"repo:read"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if a.ParentUserID != parent {
		t.Errorf("parent_user_id mismatch")
	}

	rec := store_.Recorded()[0]
	if rec.EventType != EventAgentCreated {
		t.Errorf("event_type: %q", rec.EventType)
	}
	raw, _ := json.Marshal(rec.Payload)
	var p AgentCreatedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.RateBucket == "" || p.PrincipalClass != "agent" || p.ReputationScore != 0.5 {
		t.Errorf("payload metering fields missing/wrong: %+v", p)
	}
}

func TestCreateAgent_rejectsEmptyDisplayName(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateAgent(ctx, uuid.New(), "", nil); !errors.Is(err, ErrEmptyDisplayName) {
		t.Fatalf("expected ErrEmptyDisplayName, got %v", err)
	}
}

func TestSetAgentReputationScore_clampsToZeroOne(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()
	a, _ := svc.CreateAgent(ctx, uuid.New(), "rep-test", nil)

	if err := svc.SetAgentReputationScore(ctx, a.ID, 1.7); err != nil {
		t.Fatalf("set high: %v", err)
	}
	got, _ := svc.GetAgent(ctx, a.ID)
	if got.ReputationScore != 1.0 {
		t.Errorf("clamp high: got %v want 1.0", got.ReputationScore)
	}

	if err := svc.SetAgentReputationScore(ctx, a.ID, -0.5); err != nil {
		t.Fatalf("set low: %v", err)
	}
	got, _ = svc.GetAgent(ctx, a.ID)
	if got.ReputationScore != 0.0 {
		t.Errorf("clamp low: got %v want 0.0", got.ReputationScore)
	}

	// agent.created + 2 reputation_updated outbox rows.
	if len(store_.Recorded()) != 3 {
		t.Errorf("expected 3 outbox rows, got %d", len(store_.Recorded()))
	}
}

func TestSetAgentReputationScore_emitsDeltaPayload(t *testing.T) {
	svc, store_ := newTestService(t)
	ctx := context.Background()
	a, _ := svc.CreateAgent(ctx, uuid.New(), "delta-test", nil)

	if err := svc.SetAgentReputationScore(ctx, a.ID, 0.8); err != nil {
		t.Fatalf("set: %v", err)
	}
	rec := store_.Recorded()[1]
	if rec.EventType != EventAgentReputationUpdated {
		t.Fatalf("event_type: %q", rec.EventType)
	}
	raw, _ := json.Marshal(rec.Payload)
	var p AgentReputationUpdatedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.OldScore != 0.5 || p.NewScore != 0.8 {
		t.Errorf("scores: old=%v new=%v", p.OldScore, p.NewScore)
	}
	wantDelta := 0.8 - 0.5
	if p.Delta < wantDelta-1e-9 || p.Delta > wantDelta+1e-9 {
		t.Errorf("delta: got %v want %v", p.Delta, wantDelta)
	}
}

func TestSetAgentReputationScore_unknownAgent(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.SetAgentReputationScore(ctx, uuid.New(), 0.5); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestGetUserByEmail_caseInsensitive(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, "Mixed@Case.Email", "pw"); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetUserByEmail(ctx, "MIXED@case.email")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got == nil || got.Email != "mixed@case.email" {
		t.Errorf("case-insensitive lookup failed: %v", got)
	}
}

func TestGetAgentsByParentUser_returnsAll(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	parent := uuid.New()
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateAgent(ctx, parent, "a", nil); err != nil {
			t.Fatal(err)
		}
	}
	// One unrelated agent.
	_, _ = svc.CreateAgent(ctx, uuid.New(), "other", nil)

	got, err := svc.GetAgentsByParentUser(ctx, parent)
	if err != nil {
		t.Fatalf("GetAgentsByParentUser: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 agents, got %d", len(got))
	}
}

func TestLookupIdentityForCache_returnsCacheEntry(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	u, _ := svc.CreateUser(ctx, "cache@test.dev", "pw")

	entry, err := svc.LookupIdentityForCache(ctx, u.ID)
	if err != nil {
		t.Fatalf("LookupIdentityForCache: %v", err)
	}
	if entry == nil || entry.PrincipalID != u.ID.String() {
		t.Errorf("entry: %+v", entry)
	}
}

func TestRevocationMethods_returnNotImplemented(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	tests := map[string]error{
		"DisableUser":          svc.DisableUser(ctx, uuid.New(), "x"),
		"RevokeAgent":          svc.RevokeAgent(ctx, uuid.New(), "x"),
		"UpdateAgentPerms":     svc.UpdateAgentPermissions(ctx, uuid.New(), []string{"r"}),
		"AddOrgMember":         svc.AddOrgMember(ctx, uuid.New(), uuid.New(), "owner"),
		"RemoveOrgMember":      svc.RemoveOrgMember(ctx, uuid.New(), uuid.New()),
	}
	for name, err := range tests {
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s: expected ErrNotImplemented, got %v", name, err)
		}
	}
}

func TestCredentialHasher_roundTrip(t *testing.T) {
	h := newArgon2idHasherFast()
	hashed, err := h.Hash("pw1234")
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := h.Verify("pw1234", hashed)
	if !ok {
		t.Error("Verify rejected matching plaintext")
	}
	bad, _ := h.Verify("wrong", hashed)
	if bad {
		t.Error("Verify accepted wrong plaintext")
	}
}

func TestCredentialHasher_needsRehashWhenParamsDriftWeak(t *testing.T) {
	weak := newArgon2idHasherFast()
	strong := NewArgon2idHasher()
	hashed, _ := weak.Hash("pw")
	ok, needsRehash := strong.Verify("pw", hashed)
	if !ok {
		t.Fatal("Verify should accept correct plaintext")
	}
	if !needsRehash {
		t.Error("Verify should flag needsRehash when stored hash uses weaker params")
	}
}

func TestCredentialHasher_emptyHash(t *testing.T) {
	h := newArgon2idHasherFast()
	if _, err := h.Hash(""); err != nil {
		t.Errorf("Hash(\"\") should not error per current API: %v", err)
	}
	// And CreateUser should still reject.
	svc, _ := newTestService(t)
	if _, err := svc.CreateUser(context.Background(), "e@x.com", ""); !errors.Is(err, ErrCredentialEmpty) {
		t.Errorf("expected ErrCredentialEmpty, got %v", err)
	}
}

func TestWithSerializableRetry_succeedsImmediately(t *testing.T) {
	calls := 0
	err := WithSerializableRetry(context.Background(), func() error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Errorf("calls=%d err=%v", calls, err)
	}
}

func TestWithSerializableRetry_propagatesNonRetryable(t *testing.T) {
	sentinel := errors.New("non-retryable")
	calls := 0
	err := WithSerializableRetry(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Errorf("calls=%d err=%v", calls, err)
	}
}
