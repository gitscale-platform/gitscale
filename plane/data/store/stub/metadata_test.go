package stub_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

func TestStub_TransactCommit(t *testing.T) {
	s := stub.New()
	ctx := context.Background()
	userID := uuid.New()

	err := s.Transact(ctx, func(tx store.Tx) error {
		return tx.Identity().InsertHumanUser(ctx, store.HumanUser{
			ID:             userID,
			Email:          "test@example.com",
			CredentialHash: "hash",
			RateBucket:     "human_default",
		})
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}

	u, err := s.Identity().GetUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u == nil || u.Email != "test@example.com" {
		t.Fatalf("expected user to be committed, got %v", u)
	}
}

func TestStub_TransactRollback(t *testing.T) {
	s := stub.New()
	ctx := context.Background()
	userID := uuid.New()
	sentinel := errors.New("intentional rollback")

	err := s.Transact(ctx, func(tx store.Tx) error {
		_ = tx.Identity().InsertHumanUser(ctx, store.HumanUser{
			ID:    userID,
			Email: "rollback@example.com",
		})
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}

	u, err := s.Identity().GetUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserByID after rollback: %v", err)
	}
	if u != nil {
		t.Fatal("expected no user after rollback")
	}
}

func TestStub_WriteOutbox(t *testing.T) {
	s := stub.New()
	ctx := context.Background()
	aggID := uuid.New()

	err := s.Transact(ctx, func(tx store.Tx) error {
		return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", aggID, "user.created", map[string]string{"foo": "bar"})
	})
	if err != nil {
		t.Fatalf("Transact with WriteOutbox: %v", err)
	}

	records := s.Recorded()
	if len(records) != 1 {
		t.Fatalf("expected 1 outbox record, got %d", len(records))
	}
	r := records[0]
	if r.Domain != store.DomainIdentity {
		t.Errorf("domain: got %q want %q", r.Domain, store.DomainIdentity)
	}
	if r.EventType != "user.created" {
		t.Errorf("event_type: got %q want %q", r.EventType, "user.created")
	}
	if r.AggregateID != aggID {
		t.Errorf("aggregate_id: got %v want %v", r.AggregateID, aggID)
	}
	if r.EventID == uuid.Nil {
		t.Error("event_id should not be nil")
	}
}

func TestStub_WriteOutbox_RollbackDiscardsRecord(t *testing.T) {
	s := stub.New()
	ctx := context.Background()
	aggID := uuid.New()
	sentinel := errors.New("rollback")

	_ = s.Transact(ctx, func(tx store.Tx) error {
		_ = tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", aggID, "user.created", nil)
		return sentinel
	})

	if len(s.Recorded()) != 0 {
		t.Fatal("expected no outbox records after rollback")
	}
}

func TestStub_WriteOutbox_InvalidDomain(t *testing.T) {
	s := stub.New()
	ctx := context.Background()
	err := s.Transact(ctx, func(tx store.Tx) error {
		return tx.WriteOutbox(ctx, "bogus", "t", uuid.New(), "e", nil)
	})
	if err == nil {
		t.Fatal("expected error for invalid domain")
	}
}
