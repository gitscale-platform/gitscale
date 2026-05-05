package kafka

import (
	"encoding/json"
	"time"
)

// EventEnvelope is the wire format for every event published to a Kafka topic
// by the polling outbox consumer (ADR-008).
//
// The partition key on every topic is AggregateID (ADR-004).
// SchemaVersion refers to the payload schema version, not the topic version.
// See plane/data/kafka/topics.yaml §versioning-policy for the topic versioning
// contract (in-place compatible evolution; breaking changes roll to …events.v2).
type EventEnvelope struct {
	// EventID is the UUID from outbox.event_id. Consumers use this for
	// idempotency deduplication (ADR-008).
	EventID string `json:"event_id"`

	// AggregateType names the domain aggregate, e.g. "pull_request", "repository".
	AggregateType string `json:"aggregate_type"`

	// AggregateID is the UUID of the aggregate. This is the Kafka partition key
	// for all topics (ADR-004), preserving per-aggregate ordering.
	AggregateID string `json:"aggregate_id"`

	// EventType is the dot-separated event name, e.g. "pr.opened", "repo.archived".
	// Pattern: ^[a-z_]+\.[a-z_]+$
	EventType string `json:"event_type"`

	// SchemaVersion is the payload schema version, e.g. "v1".
	// Incremented only on breaking payload changes; most events remain at "v1".
	SchemaVersion string `json:"schema_version"`

	// Payload is the domain-specific event body. Shape is defined in
	// plane/data/events/<domain>/<event_type>.schema.json.
	Payload json.RawMessage `json:"payload"`

	// OccurredAt is when the domain event occurred — copied from the source
	// transaction timestamp, not from Kafka publish time. Use this for
	// event-time ordering and reconciliation windows.
	OccurredAt time.Time `json:"occurred_at"`

	// PublishedAt is when the outbox consumer published the event to Kafka
	// (RFC3339, UTC). Delta between OccurredAt and PublishedAt is the outbox
	// consumer lag for this event.
	PublishedAt time.Time `json:"published_at"`

	// PrincipalType identifies what kind of principal caused the event:
	// "user", "agent", or "service". Carried from the JWT-SVID claims (ADR-010)
	// so downstream consumers (billing, CI routing) can branch without a
	// secondary identity lookup.
	PrincipalType string `json:"principal_type"`

	// RateBucket is the billing routing key (e.g. "agent_pro", "human_default").
	// Carried from JWT-SVID claims (ADR-010) so BillingAggregator can route
	// usage records without re-resolving identity.
	RateBucket string `json:"rate_bucket"`
}

// MarshalJSON encodes the envelope to JSON bytes.
// Provided as a helper so callers don't need to import encoding/json directly.
func (e EventEnvelope) MarshalJSON() ([]byte, error) {
	type alias EventEnvelope
	b, err := json.Marshal(alias(e))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// UnmarshalEnvelope decodes JSON bytes into an EventEnvelope.
func UnmarshalEnvelope(data []byte) (EventEnvelope, error) {
	var e EventEnvelope
	if err := json.Unmarshal(data, &e); err != nil {
		return EventEnvelope{}, err
	}
	return e, nil
}
