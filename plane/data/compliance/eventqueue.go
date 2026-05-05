// Package compliance contains the ADR-017 contract test suites for CacheStore,
// RateLimiter, and EventQueue (KafkaProducer). All implementations must pass
// every case in these suites.
package compliance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/google/uuid"
)

// KafkaProducerFactory constructs a fresh KafkaProducer for each sub-test.
// The returned cleanup function is called when the test ends.
type KafkaProducerFactory func(t *testing.T) (prod outbox.KafkaProducer, cleanup func())

// RunKafkaProducerCompliance runs the EventQueue / KafkaProducer contract test
// suite (ADR-017). Call this from both the real and mock producer test files.
func RunKafkaProducerCompliance(t *testing.T, topic string, factory KafkaProducerFactory) {
	t.Helper()

	t.Run("publish_empty_batch_is_noop", func(t *testing.T) {
		t.Parallel()
		prod, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		if err := prod.PublishBatch(ctx, topic, nil); err != nil {
			t.Fatalf("PublishBatch(nil): %v", err)
		}
		if err := prod.PublishBatch(ctx, topic, []outbox.OutboxRow{}); err != nil {
			t.Fatalf("PublishBatch(empty): %v", err)
		}
	})

	t.Run("publish_single_row_succeeds", func(t *testing.T) {
		t.Parallel()
		prod, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		row := outboxRow(t)
		if err := prod.PublishBatch(ctx, topic, []outbox.OutboxRow{row}); err != nil {
			t.Fatalf("PublishBatch: %v", err)
		}
	})

	t.Run("publish_batch_succeeds", func(t *testing.T) {
		t.Parallel()
		prod, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		batch := make([]outbox.OutboxRow, 10)
		for i := range batch {
			batch[i] = outboxRow(t)
		}
		if err := prod.PublishBatch(ctx, topic, batch); err != nil {
			t.Fatalf("PublishBatch: %v", err)
		}
	})

	t.Run("canceled_context_returns_error", func(t *testing.T) {
		t.Parallel()
		prod, cleanup := factory(t)
		defer cleanup()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled

		row := outboxRow(t)
		err := prod.PublishBatch(ctx, topic, []outbox.OutboxRow{row})
		if err == nil {
			t.Fatal("expected error on cancelled context, got nil")
		}
	})

	t.Run("close_is_idempotent", func(t *testing.T) {
		t.Parallel()
		prod, cleanup := factory(t)
		defer cleanup()

		if err := prod.Close(); err != nil {
			t.Fatalf("Close (1st): %v", err)
		}
		// Second close should not panic; error is acceptable.
		_ = prod.Close()
	})
}

func outboxRow(t *testing.T) outbox.OutboxRow {
	t.Helper()
	return outbox.OutboxRow{
		ID:            1,
		EventID:       uuid.New(),
		AggregateType: "TestAggregate",
		AggregateID:   uuid.New(),
		EventType:     "test.created",
		Payload:       json.RawMessage(`{"key":"value"}`),
		CreatedAt:     time.Now().UTC(),
	}
}
