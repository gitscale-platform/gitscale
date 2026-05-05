package outbox

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPollInterval   = 1000 * time.Millisecond
	defaultPublishTimeout = 5000 * time.Millisecond
	defaultBatchSize      = 100
)

// Config holds the runtime configuration for one OutboxConsumer instance.
// One instance per domain; each is independent (ADR-008).
type Config struct {
	// Domain is the schema domain name, e.g. "identity". Used in metrics labels
	// and the advisory-lock key.
	Domain string

	// Table is the schema-qualified outbox table name,
	// e.g. "identity.identity_outbox".
	Table string

	// Topic is the Kafka topic to publish events to,
	// e.g. "gitscale.identity.events".
	Topic string

	// DB is the pgxpool used for all SQL in the drain loop. The consumer does
	// not own the pool's lifecycle; the caller is responsible for Close.
	DB *pgxpool.Pool

	// Producer is the Kafka producer wrapper used for publishing.
	Producer KafkaProducer

	// PollInterval is the time between poll cycles.
	// Env: OUTBOX_POLL_INTERVAL_MS. Default: 1000ms.
	PollInterval time.Duration

	// PublishTimeout is the per-batch Kafka publish deadline. The transaction
	// context is also bounded by this duration.
	// Env: OUTBOX_PUBLISH_TIMEOUT_MS. Default: 5000ms.
	PublishTimeout time.Duration

	// BatchSize is the LIMIT on the SELECT FOR UPDATE SKIP LOCKED query.
	// Env: OUTBOX_BATCH_SIZE. Default: 100.
	BatchSize int

	// Metrics holds the Prometheus metric handles. Nil-safe — all recording
	// calls guard against a nil Metrics pointer.
	Metrics *Metrics
}

// ApplyEnvDefaults fills any zero-value duration/int fields in c from
// environment variables, then falls back to hard-coded defaults. Call once
// after constructing each Config.
func (c *Config) ApplyEnvDefaults() error {
	if c.PollInterval == 0 {
		ms, err := envDurationMS("OUTBOX_POLL_INTERVAL_MS", defaultPollInterval)
		if err != nil {
			return fmt.Errorf("config: OUTBOX_POLL_INTERVAL_MS: %w", err)
		}
		c.PollInterval = ms
	}
	if c.PublishTimeout == 0 {
		ms, err := envDurationMS("OUTBOX_PUBLISH_TIMEOUT_MS", defaultPublishTimeout)
		if err != nil {
			return fmt.Errorf("config: OUTBOX_PUBLISH_TIMEOUT_MS: %w", err)
		}
		c.PublishTimeout = ms
	}
	if c.BatchSize == 0 {
		n, err := envInt("OUTBOX_BATCH_SIZE", defaultBatchSize)
		if err != nil {
			return fmt.Errorf("config: OUTBOX_BATCH_SIZE: %w", err)
		}
		c.BatchSize = n
	}
	return nil
}

func envDurationMS(key string, def time.Duration) (time.Duration, error) {
	s := os.Getenv(key)
	if s == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return time.Duration(n) * time.Millisecond, nil
}

func envInt(key string, def int) (int, error) {
	s := os.Getenv(key)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return n, nil
}
