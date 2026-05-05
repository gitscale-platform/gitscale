package kafka

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventEnvelope is the wire format written to every Kafka topic fed by the
// outbox consumer. Consumers must dedupe on EventID (ADR-008).
//
// Subject naming: gitscale.<domain>.<aggregate>.v<N>
type EventEnvelope struct {
	// EventID is the stable, globally-unique identifier for this event.
	// Consumers dedupe on this field.
	EventID uuid.UUID `json:"event_id"`

	// AggregateType is the name of the aggregate that emitted the event,
	// e.g. "HumanUser", "Repository", "PullRequest".
	AggregateType string `json:"aggregate_type"`

	// AggregateID is the UUID of the specific aggregate instance.
	// This is also used as the Kafka partition key (ADR-004).
	AggregateID uuid.UUID `json:"aggregate_id"`

	// EventType is the fully qualified event type name, e.g. "user.created".
	EventType string `json:"event_type"`

	// Payload is the domain-specific event body, serialised as JSON.
	Payload json.RawMessage `json:"payload"`

	// PublishedAt is the wall-clock time the outbox consumer published the
	// event to Kafka. It is not the source transaction time; use CreatedAt
	// for causal ordering.
	PublishedAt time.Time `json:"published_at"`

	// CreatedAt is the time the outbox row was written in the source
	// transaction. Use this for causal ordering.
	CreatedAt time.Time `json:"created_at"`
}
