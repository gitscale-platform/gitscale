package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	kafkadata "github.com/gitscale-platform/gitscale/plane/data/kafka"
	kafkago "github.com/segmentio/kafka-go"
)

// KafkaProducerConfig holds the connection and behaviour settings for the
// segmentio/kafka-go-backed producer.
//
// Delivery semantics: the Writer is configured for synchronous, acked writes
// (RequiredAcks = RequireAll). Within a session the broker deduplicates in
// order; across restarts, the consumer must dedupe on event_id (ADR-008).
type KafkaProducerConfig struct {
	// BootstrapServers is a comma-separated list of broker addresses.
	BootstrapServers string

	// ClientID is a human-readable name for this producer instance, used for
	// log correlation on the broker side.
	ClientID string

	// DialFunc, if non-nil, overrides the default TCP dial function used by
	// the underlying transport. Useful in tests where broker metadata returns
	// container-internal addresses (testcontainers bridge networking).
	DialFunc func(ctx context.Context, network, address string) (net.Conn, error)
}

// kafkaWriter is the concrete segmentio/kafka-go implementation of KafkaProducer.
type kafkaWriter struct {
	writer *kafkago.Writer
}

// NewKafkaProducer constructs a new KafkaProducer backed by segmentio/kafka-go.
//
// The writer is configured for:
//   - RequireAll acks (all in-sync replicas must acknowledge)
//   - Automatic topic creation is NOT assumed; topics must exist
//   - Compression: Lz4 (available in pure Go)
//   - Balancer: key-based (aggregate_id bytes), so per-aggregate ordering is
//     preserved across partitions (ADR-004)
func NewKafkaProducer(cfg KafkaProducerConfig) (KafkaProducer, error) {
	if cfg.BootstrapServers == "" {
		return nil, fmt.Errorf("NewKafkaProducer: BootstrapServers must not be empty")
	}
	transport := &kafkago.Transport{}
	if cfg.DialFunc != nil {
		transport.Dial = cfg.DialFunc
	}

	w := &kafkago.Writer{
		Addr:         kafkago.TCP(cfg.BootstrapServers),
		Balancer:     &kafkago.Hash{},
		RequiredAcks: kafkago.RequireAll,
		Compression:  kafkago.Lz4,
		// WriteTimeout bounds individual batch delivery.
		WriteTimeout: 5 * time.Second,
		// Allow the writer to create topics if auto-create is enabled on the broker.
		AllowAutoTopicCreation: true,
		Transport:              transport,
	}
	return &kafkaWriter{writer: w}, nil
}

// PublishBatch serialises each OutboxRow into an EventEnvelope and writes the
// batch to the given topic. The Kafka message key is the AggregateID bytes so
// that records for the same aggregate land on the same partition (ADR-004).
//
// On any error the entire batch is considered not published. No partial success
// is reported; the caller retries the whole batch on the next poll cycle.
func (k *kafkaWriter) PublishBatch(ctx context.Context, topic string, batch []OutboxRow) error {
	k.writer.Topic = topic
	msgs := make([]kafkago.Message, 0, len(batch))
	now := time.Now().UTC()

	for _, row := range batch {
		env := kafkadata.EventEnvelope{
			EventID:       row.EventID,
			AggregateType: row.AggregateType,
			AggregateID:   row.AggregateID,
			EventType:     row.EventType,
			Payload:       row.Payload,
			PublishedAt:   now,
			CreatedAt:     row.CreatedAt,
		}
		val, err := json.Marshal(env)
		if err != nil {
			return fmt.Errorf("PublishBatch: marshal event %s: %w", row.EventID, err)
		}
		// Partition key = aggregate_id bytes (ADR-004).
		key := row.AggregateID[:]
		msgs = append(msgs, kafkago.Message{
			Key:   key,
			Value: val,
		})
	}

	if err := k.writer.WriteMessages(ctx, msgs...); err != nil {
		return fmt.Errorf("PublishBatch: write messages: %w", err)
	}
	return nil
}

// Close flushes and closes the underlying writer.
func (k *kafkaWriter) Close() error {
	if err := k.writer.Close(); err != nil {
		return fmt.Errorf("kafkaWriter.Close: %w", err)
	}
	return nil
}
