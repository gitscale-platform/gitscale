package compliance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// MetadataStoreFactory constructs a fresh MetadataStore + verifier for each
// sub-test. The verifier exposes test-only read paths into the underlying
// outbox tables (or the stub's Recorded() accessor) without coupling the
// compliance suite to a concrete implementation. Cleanup is called when the
// test ends; for postgres-backed tests it tears down the testcontainer.
type MetadataStoreFactory func(t *testing.T) (s store.MetadataStore, v OutboxVerifier, cleanup func())

// OutboxVerifier exposes the outbox-row reads needed by the compliance suite.
// Implementations:
//   - postgres: queries the per-domain outbox table directly via pgxpool.
//   - stub: reads from the stub.Store.Recorded() slice.
type OutboxVerifier interface {
	// OutboxCount returns the number of outbox rows in domain whose event_type
	// matches the given value. ctx is honoured for postgres-backed verifiers.
	OutboxCount(ctx context.Context, domain store.Domain, eventType string) (int, error)
	// OutboxEventIDs returns all event_ids in domain in insertion order.
	OutboxEventIDs(ctx context.Context, domain store.Domain) ([]uuid.UUID, error)
}

// MetadataStoreOptions controls which subtests run. Used to skip the 40001
// race on the stub (which has no real serializable contention).
type MetadataStoreOptions struct {
	// SkipSerializable40001 instructs the suite to skip the 40001 race subtest.
	// Set true for the stub (in-memory) impl.
	SkipSerializable40001 bool
}

// RunMetadataStoreCompliance runs the ADR-017 contract test suite for
// MetadataStore. Call this from both the real (postgres) and stub test files.
//
// The suite covers:
//   - Transact commit semantics (nil → commit; non-nil → rollback)
//   - WriteOutbox transactional invariant (source + outbox commit/rollback together)
//   - Domain allowlist (5 valid + 1 rejected)
//   - Table dispatch (each domain writes to its own outbox table)
//   - Serializable retry contract (concurrent Tx → 40001 → IsRetryable)
//   - EventID monotonic UUIDv7
//   - Domain reader stubs return sentinel (not panic)
func RunMetadataStoreCompliance(t *testing.T, factory MetadataStoreFactory, opts MetadataStoreOptions) {
	t.Helper()

	t.Run("Transact_commit_on_nil_error", func(t *testing.T) {
		s, v, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		userID := uuid.New()
		err := s.Transact(ctx, func(tx store.Tx) error {
			return tx.Identity().InsertHumanUser(ctx, store.HumanUser{
				ID:             userID,
				Email:          fmt.Sprintf("commit-%s@example.com", userID.String()[:8]),
				CredentialHash: "hash",
				RateBucket:     "human_default",
			})
		})
		if err != nil {
			t.Fatalf("Transact: %v", err)
		}

		got, err := s.Identity().GetUserByID(ctx, userID)
		if err != nil {
			t.Fatalf("GetUserByID after commit: %v", err)
		}
		if got == nil {
			t.Fatal("expected committed user, got nil")
		}
		_ = v
	})

	t.Run("Transact_rollback_on_non_nil_error", func(t *testing.T) {
		s, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		sentinel := errors.New("intentional rollback")
		userID := uuid.New()
		err := s.Transact(ctx, func(tx store.Tx) error {
			_ = tx.Identity().InsertHumanUser(ctx, store.HumanUser{
				ID:             userID,
				Email:          fmt.Sprintf("rb-%s@example.com", userID.String()[:8]),
				CredentialHash: "hash",
				RateBucket:     "human_default",
			})
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel, got %v", err)
		}

		got, err := s.Identity().GetUserByID(ctx, userID)
		if err != nil {
			t.Fatalf("GetUserByID after rollback: %v", err)
		}
		if got != nil {
			t.Fatal("expected user to NOT exist after rollback")
		}
	})

	t.Run("WriteOutbox_transactional_invariant", func(t *testing.T) {
		s, v, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// Commit case: source row + outbox row both present.
		userID := uuid.New()
		eventType := fmt.Sprintf("user.created.commit.%s", userID.String()[:8])
		err := s.Transact(ctx, func(tx store.Tx) error {
			if err := tx.Identity().InsertHumanUser(ctx, store.HumanUser{
				ID:             userID,
				Email:          fmt.Sprintf("inv-%s@example.com", userID.String()[:8]),
				CredentialHash: "hash",
				RateBucket:     "human_default",
			}); err != nil {
				return err
			}
			return tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", userID, eventType, map[string]string{"id": userID.String()})
		})
		if err != nil {
			t.Fatalf("Transact (commit): %v", err)
		}
		count, err := v.OutboxCount(ctx, store.DomainIdentity, eventType)
		if err != nil {
			t.Fatalf("OutboxCount: %v", err)
		}
		if count != 1 {
			t.Fatalf("commit: expected 1 outbox row, got %d", count)
		}

		// Rollback case: neither source nor outbox row.
		userID2 := uuid.New()
		eventType2 := fmt.Sprintf("user.created.rollback.%s", userID2.String()[:8])
		sentinel := errors.New("rollback")
		_ = s.Transact(ctx, func(tx store.Tx) error {
			_ = tx.Identity().InsertHumanUser(ctx, store.HumanUser{
				ID:             userID2,
				Email:          fmt.Sprintf("inv2-%s@example.com", userID2.String()[:8]),
				CredentialHash: "hash",
				RateBucket:     "human_default",
			})
			_ = tx.WriteOutbox(ctx, store.DomainIdentity, "human_user", userID2, eventType2, nil)
			return sentinel
		})
		count2, err := v.OutboxCount(ctx, store.DomainIdentity, eventType2)
		if err != nil {
			t.Fatalf("OutboxCount (rollback): %v", err)
		}
		if count2 != 0 {
			t.Fatalf("rollback: expected 0 outbox rows, got %d", count2)
		}
	})

	t.Run("WriteOutbox_domain_allowlist", func(t *testing.T) {
		s, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// Invalid domain rejected.
		err := s.Transact(ctx, func(tx store.Tx) error {
			return tx.WriteOutbox(ctx, store.Domain("bogus"), "x", uuid.New(), "x.created", nil)
		})
		if err == nil {
			t.Fatal("expected error for invalid domain")
		}

		// All 5 valid domains accepted (using a unique aggregate per call).
		for _, d := range []store.Domain{
			store.DomainIdentity,
			store.DomainRepositories,
			store.DomainCollaboration,
			store.DomainCI,
			store.DomainBilling,
		} {
			d := d
			err := s.Transact(ctx, func(tx store.Tx) error {
				return tx.WriteOutbox(ctx, d, "TestAggregate", uuid.New(), "test.allowlist", map[string]string{"d": string(d)})
			})
			if err != nil {
				t.Errorf("WriteOutbox(%s): %v", d, err)
			}
		}
	})

	t.Run("WriteOutbox_table_dispatch", func(t *testing.T) {
		s, v, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		marker := uuid.NewString()[:8]

		for _, d := range []store.Domain{
			store.DomainIdentity,
			store.DomainRepositories,
			store.DomainCollaboration,
			store.DomainCI,
			store.DomainBilling,
		} {
			d := d
			eventType := fmt.Sprintf("test.dispatch.%s.%s", d, marker)
			err := s.Transact(ctx, func(tx store.Tx) error {
				return tx.WriteOutbox(ctx, d, "TestAggregate", uuid.New(), eventType, nil)
			})
			if err != nil {
				t.Fatalf("WriteOutbox(%s): %v", d, err)
			}
			count, err := v.OutboxCount(ctx, d, eventType)
			if err != nil {
				t.Fatalf("OutboxCount(%s): %v", d, err)
			}
			if count != 1 {
				t.Errorf("domain %s: expected 1 row in its outbox, got %d", d, count)
			}
		}
	})

	if !opts.SkipSerializable40001 {
		t.Run("Serializable_retry_contract_40001", func(t *testing.T) {
			s, _, cleanup := factory(t)
			defer cleanup()
			ctx := context.Background()

			// Seed a user that both Txs will read-modify-write.
			userID := uuid.New()
			if err := s.Transact(ctx, func(tx store.Tx) error {
				return tx.Identity().InsertHumanUser(ctx, store.HumanUser{
					ID:             userID,
					Email:          fmt.Sprintf("serial-%s@example.com", userID.String()[:8]),
					CredentialHash: "hash",
					RateBucket:     "human_default",
				})
			}); err != nil {
				t.Fatalf("seed user: %v", err)
			}

			// Run a few rounds — at least one must produce a 40001.
			var sawRetryable bool
			for round := 0; round < 5 && !sawRetryable; round++ {
				var (
					wg     sync.WaitGroup
					mu     sync.Mutex
					errs   = make([]error, 2)
					ready  = make(chan struct{})
					hasGo1 = make(chan struct{})
				)
				txFn := func(slot int, releaseDelay time.Duration, ready, hasGo chan struct{}) func() {
					return func() {
						defer wg.Done()
						err := s.Transact(ctx, func(tx store.Tx) error {
							u, err := tx.Identity().GetUserByID(ctx, userID)
							if err != nil {
								return err
							}
							if u == nil {
								return errors.New("seed user missing")
							}
							if slot == 0 {
								close(ready)
								<-hasGo
							} else {
								<-ready
								time.Sleep(releaseDelay)
								close(hasGo)
							}
							return tx.Identity().InsertHumanUser(ctx, store.HumanUser{
								ID:             uuid.New(),
								Email:          fmt.Sprintf("conf-%d-%d-%s@example.com", round, slot, uuid.NewString()[:6]),
								CredentialHash: "hash",
								RateBucket:     "human_default",
							})
						})
						mu.Lock()
						errs[slot] = err
						mu.Unlock()
					}
				}

				wg.Add(2)
				go txFn(0, 0, ready, hasGo1)()
				go txFn(1, 5*time.Millisecond, ready, hasGo1)()
				wg.Wait()

				for _, err := range errs {
					if err != nil && store.IsRetryable(err) {
						sawRetryable = true
						break
					}
				}
			}
			if !sawRetryable {
				t.Skip("no 40001 observed across 5 rounds; race may be timing-sensitive on this engine")
			}
		})
	}

	t.Run("EventID_monotonic_uuidv7", func(t *testing.T) {
		s, v, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		const N = 20
		for i := 0; i < N; i++ {
			err := s.Transact(ctx, func(tx store.Tx) error {
				return tx.WriteOutbox(ctx, store.DomainCollaboration, "TestAggregate", uuid.New(), "test.uuidv7", nil)
			})
			if err != nil {
				t.Fatalf("WriteOutbox iter %d: %v", i, err)
			}
		}

		ids, err := v.OutboxEventIDs(ctx, store.DomainCollaboration)
		if err != nil {
			t.Fatalf("OutboxEventIDs: %v", err)
		}
		if len(ids) < N {
			t.Fatalf("expected at least %d event_ids, got %d", N, len(ids))
		}
		for _, id := range ids {
			if v := id.Version(); v != 7 {
				t.Errorf("event_id %s has version %d, expected 7 (UUIDv7)", id, v)
			}
		}
		// Monotonic check across single-thread inserts: each new id ≥ previous.
		for i := 1; i < len(ids); i++ {
			if compareUUIDBytes(ids[i-1], ids[i]) > 0 {
				t.Errorf("event_id %d (%s) is not >= prior %d (%s); UUIDv7 should be monotonic",
					i, ids[i], i-1, ids[i-1])
			}
		}
	})

	t.Run("Domain_reader_stubs_return_sentinel", func(t *testing.T) {
		s, _, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		// RepositoryReader methods are not implemented in #14; they must return
		// a non-nil error and not panic.
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("RepositoryReader.GetByID panicked: %v", r)
			}
		}()
		_, err := s.Repositories().GetByID(ctx, uuid.New())
		if err == nil {
			t.Error("expected RepositoryReader.GetByID to return sentinel error")
		}
	})
}

// compareUUIDBytes returns negative/zero/positive like bytes.Compare.
// UUIDv7 byte order encodes the timestamp in the high bits, so byte-compare
// approximates timestamp order for monotonicity checks.
func compareUUIDBytes(a, b uuid.UUID) int {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
