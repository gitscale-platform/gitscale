// Package outbox implements the polling-based outbox consumer that drains
// each domain's *_outbox table and publishes events to Kafka (ADR-008).
//
// The KafkaProducer interface is the only seam between the consumer loop and
// the Kafka transport layer. The concrete implementation (producer_kafka.go)
// uses segmentio/kafka-go. Tests use producer_mock.go.
package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// OutboxRow is a single row read from a domain's outbox table.
type OutboxRow struct {
	// ID is the BIGSERIAL primary key of the outbox row. Used in the batch UPDATE.
	ID int64

	// EventID is the globally unique identifier for this event.
	// Consumers must dedupe on this field (ADR-008).
	EventID uuid.UUID

	// AggregateType names the aggregate that emitted the event.
	AggregateType string

	// AggregateID is the partition key sent to Kafka (ADR-004).
	AggregateID uuid.UUID

	// EventType is the domain-specific event type name.
	EventType string

	// Payload is the event body as raw JSON.
	Payload json.RawMessage

	// CreatedAt is the time the source transaction committed the outbox row.
	CreatedAt time.Time
}

// KafkaProducer is the interface consumed by the drain loop.
//
// Implementations must be safe for concurrent use but the drain loop itself
// calls PublishBatch from a single goroutine per consumer instance.
type KafkaProducer interface {
	// PublishBatch produces every event in batch to topic, then blocks until
	// every event has a successful delivery acknowledgement or ctx is
	// canceled. On any error the entire batch is considered not published;
	// the caller MUST NOT mark the rows processed — they will be retried on
	// the next poll cycle. Partial successes are not reported.
	PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error

	// Close flushes any internal buffers and releases resources. The caller
	// passes a context with a deadline; Close must respect it.
	Close() error
}
