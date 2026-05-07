package invalidator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	gitscalekafka "github.com/gitscale-platform/gitscale/plane/data/kafka"
	"github.com/google/uuid"
)

// fakeReader is a slice-backed MessageReader. Run drains it then blocks until
// ctx is cancelled.
type fakeReader struct {
	mu         sync.Mutex
	msgs       []RawMessage
	idx        int
	committed  []int64
	closeCalls int
}

func (f *fakeReader) FetchMessage(ctx context.Context) (RawMessage, error) {
	f.mu.Lock()
	if f.idx >= len(f.msgs) {
		f.mu.Unlock()
		<-ctx.Done()
		return RawMessage{}, ctx.Err()
	}
	m := f.msgs[f.idx]
	f.idx++
	f.mu.Unlock()
	return m, nil
}

func (f *fakeReader) CommitMessages(_ context.Context, msgs ...RawMessage) error {
	f.mu.Lock()
	for _, m := range msgs {
		f.committed = append(f.committed, m.Offset)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeReader) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeReader) progress() (idx, committed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idx, len(f.committed)
}

// memoryCache is a minimal cache.CacheStore for tests. Only Get/Set/Delete
// are exercised by the consumer; the rest return nil.
type memoryCache struct {
	mu      sync.Mutex
	data    map[string][]byte
	expires map[string]time.Time
	deletes []string
	getErr  error
}

func newMemoryCache() *memoryCache {
	return &memoryCache{data: map[string][]byte{}, expires: map[string]time.Time{}}
}

func (m *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.data[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return v, nil
}
func (m *memoryCache) MGet(_ context.Context, keys []string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = m.data[k]
	}
	return out, nil
}
func (m *memoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	m.expires[key] = time.Now().Add(ttl)
	return nil
}
func (m *memoryCache) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, key)
	delete(m.data, key)
	return nil
}
func (m *memoryCache) CompareAndSwap(_ context.Context, _ string, _, _ []byte, _ time.Duration) (bool, error) {
	return false, nil
}
func (m *memoryCache) Ping(_ context.Context) error { return nil }
func (m *memoryCache) deleteList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deletes))
	copy(out, m.deletes)
	return out
}

func envelope(t *testing.T, eventType, eventID string, affected []uuid.UUID) RawMessage {
	t.Helper()
	payload, err := json.Marshal(struct {
		AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
	}{affected})
	if err != nil {
		t.Fatal(err)
	}
	env := gitscalekafka.EventEnvelope{
		EventID:     eventID,
		EventType:   eventType,
		Payload:     payload,
		OccurredAt:  time.Now().UTC(),
		PublishedAt: time.Now().UTC(),
	}
	value, err := env.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return RawMessage{Topic: "gitscale.identity.events", Value: value, Offset: int64(time.Now().UnixNano())}
}

func newConsumer(t *testing.T, r *fakeReader, c cache.CacheStore) (*Consumer, *Metrics) {
	t.Helper()
	m := NewMetrics()
	cons, err := New(Config{
		Reader:  r,
		Cache:   c,
		Deduper: NewDeduper(c),
		Metrics: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cons, m
}

func runUntilDrained(t *testing.T, c *Consumer, r *fakeReader) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		idx, committed := r.progress()
		if idx >= len(r.msgs) && committed == len(r.msgs) {
			cancel()
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("timed out waiting for consumer to drain (idx=%d committed=%d msgs=%d)",
				idx, committed, len(r.msgs))
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned: %v", err)
	}
}

func TestConsumer_userDisabled_deletesIdentityKey(t *testing.T) {
	pid := uuid.New()
	r := &fakeReader{msgs: []RawMessage{envelope(t, EventUserDisabled, uuid.NewString(), []uuid.UUID{pid})}}
	c := newMemoryCache()
	cons, m := newConsumer(t, r, c)
	runUntilDrained(t, cons, r)

	wantKey := fmt.Sprintf(cache.IdentityKey, pid)
	if len(c.deleteList()) != 1 || c.deleteList()[0] != wantKey {
		t.Errorf("deletes=%v want [%s]", c.deleteList(), wantKey)
	}
	if got := m.InvalidationCount(EventUserDisabled, ResultOK); got != 1 {
		t.Errorf("invalidations[user.disabled,ok]=%d want 1", got)
	}
}

func TestConsumer_orgMemberRemoved_deletesEveryAffected(t *testing.T) {
	user := uuid.New()
	org := uuid.New()
	r := &fakeReader{msgs: []RawMessage{envelope(t, EventOrgMemberRemoved, uuid.NewString(), []uuid.UUID{user, org})}}
	c := newMemoryCache()
	cons, _ := newConsumer(t, r, c)
	runUntilDrained(t, cons, r)
	if len(c.deleteList()) != 2 {
		t.Errorf("deletes=%d want 2 (user + org)", len(c.deleteList()))
	}
}

func TestConsumer_replayedEventID_dedupes(t *testing.T) {
	pid := uuid.New()
	id := uuid.NewString()
	r := &fakeReader{msgs: []RawMessage{
		envelope(t, EventUserDisabled, id, []uuid.UUID{pid}),
		envelope(t, EventUserDisabled, id, []uuid.UUID{pid}), // same id
	}}
	c := newMemoryCache()
	cons, m := newConsumer(t, r, c)
	runUntilDrained(t, cons, r)
	if len(c.deleteList()) != 1 {
		t.Errorf("deletes=%d want 1 (dedupe)", len(c.deleteList()))
	}
	if got := m.InvalidationCount(EventUserDisabled, ResultAlreadyProcessed); got != 1 {
		t.Errorf("already_processed counter=%d want 1", got)
	}
}

func TestConsumer_unknownEventType_commitsAndCounts(t *testing.T) {
	r := &fakeReader{msgs: []RawMessage{envelope(t, "user.unrecognised", uuid.NewString(), []uuid.UUID{uuid.New()})}}
	c := newMemoryCache()
	cons, m := newConsumer(t, r, c)
	runUntilDrained(t, cons, r)
	if len(c.deleteList()) != 0 {
		t.Errorf("deletes=%d want 0 (unknown event type)", len(c.deleteList()))
	}
	if got := m.InvalidationCount("user.unrecognised", ResultUnknownEventType); got != 1 {
		t.Errorf("unknown_event_type counter=%d want 1", got)
	}
}

func TestConsumer_envelopeDecodeFailed_isPoison(t *testing.T) {
	r := &fakeReader{msgs: []RawMessage{{Value: []byte("not-json")}}}
	c := newMemoryCache()
	cons, m := newConsumer(t, r, c)
	runUntilDrained(t, cons, r)
	if len(c.deleteList()) != 0 {
		t.Errorf("deletes=%d want 0", len(c.deleteList()))
	}
	if got := m.InvalidationCount("", ResultEnvelopeDecode); got != 1 {
		t.Errorf("envelope_decode counter=%d want 1", got)
	}
}

func TestNew_rejectsNilCollaborators(t *testing.T) {
	cases := []Config{
		{Cache: newMemoryCache(), Deduper: NewDeduper(newMemoryCache()), Metrics: NewMetrics()},
		{Reader: &fakeReader{}, Deduper: NewDeduper(newMemoryCache()), Metrics: NewMetrics()},
		{Reader: &fakeReader{}, Cache: newMemoryCache(), Metrics: NewMetrics()},
		{Reader: &fakeReader{}, Cache: newMemoryCache(), Deduper: NewDeduper(newMemoryCache())},
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("case %d: expected error from missing collaborator", i)
		}
	}
}
