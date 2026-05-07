package invalidator

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	gitscalekafka "github.com/gitscale-platform/gitscale/plane/data/kafka"
)

// MessageReader is the subset of segmentio/kafka-go *Reader the consumer
// depends on. Tests inject an in-memory reader via this interface (see
// consumer_test.go). Production wiring constructs a kafka.Reader.
type MessageReader interface {
	FetchMessage(ctx context.Context) (RawMessage, error)
	CommitMessages(ctx context.Context, msgs ...RawMessage) error
	Close() error
}

// RawMessage is the consumer's internal projection of a Kafka message — the
// only fields it touches. Decoupling here keeps the test fake free of the
// full kafka-go Message struct.
type RawMessage struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Value     []byte
}

// Consumer drains gitscale.identity.events and invalidates the per-principal
// identity cache entry for every UUID in the payload's affected_principal_ids.
type Consumer struct {
	reader   MessageReader
	cache    cache.CacheStore
	deduper  Deduper
	handlers map[string]EventHandler
	metrics  *Metrics
}

// Config wires the Consumer's collaborators. All fields required.
type Config struct {
	Reader   MessageReader
	Cache    cache.CacheStore
	Deduper  Deduper
	Handlers map[string]EventHandler
	Metrics  *Metrics
}

// New constructs a Consumer. Returns an error if any collaborator is nil.
func New(cfg Config) (*Consumer, error) {
	switch {
	case cfg.Reader == nil:
		return nil, errors.New("invalidator: Config.Reader is nil")
	case cfg.Cache == nil:
		return nil, errors.New("invalidator: Config.Cache is nil")
	case cfg.Deduper == nil:
		return nil, errors.New("invalidator: Config.Deduper is nil")
	case cfg.Metrics == nil:
		return nil, errors.New("invalidator: Config.Metrics is nil")
	}
	handlers := cfg.Handlers
	if handlers == nil {
		handlers = DefaultHandlers
	}
	return &Consumer{
		reader:   cfg.Reader,
		cache:    cfg.Cache,
		deduper:  cfg.Deduper,
		handlers: handlers,
		metrics:  cfg.Metrics,
	}, nil
}

// Run drains messages until ctx is cancelled. Handler / cache errors retry
// the same offset (no commit); the loop returns ctx.Err() on cancellation.
// FetchMessage error other than ctx-cancel propagates so the supervisor can
// restart the process — matches the outbox consumer's failure model.
func (c *Consumer) Run(ctx context.Context) error {
	defer func() { _ = c.reader.Close() }()
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx.Err()
			}
			return fmt.Errorf("invalidator: FetchMessage: %w", err)
		}
		if err := c.processOnce(ctx, msg); err != nil {
			// Retryable: do NOT commit; loop will refetch the same offset.
			continue
		}
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			return fmt.Errorf("invalidator: CommitMessages: %w", err)
		}
	}
}

// processOnce decodes, dedupes, dispatches, deletes, and marks. Returns nil
// to signal "commit this offset"; non-nil to retry.
func (c *Consumer) processOnce(ctx context.Context, msg RawMessage) error {
	env, err := gitscalekafka.UnmarshalEnvelope(msg.Value)
	if err != nil {
		// Bad payload: increment metric and commit so we do not loop on
		// a poison pill. A future DLQ writer would attach the bytes here.
		c.metrics.IncDLQ("", ResultEnvelopeDecode)
		c.metrics.IncInvalidation("", ResultEnvelopeDecode)
		return nil
	}

	seen, err := c.deduper.Seen(ctx, env.EventID)
	if err != nil {
		c.metrics.IncInvalidation(env.EventType, ResultCacheError)
		return err
	}
	if seen {
		c.metrics.IncInvalidation(env.EventType, ResultAlreadyProcessed)
		return nil
	}

	handler := c.handlers[env.EventType]
	if handler == nil {
		c.metrics.IncInvalidation(env.EventType, ResultUnknownEventType)
		return nil
	}

	affected, err := handler.Affected(env)
	if err != nil {
		c.metrics.IncInvalidation(env.EventType, ResultHandlerError)
		// Decode failures inside a known event type are a contract violation;
		// treat as poison and commit to avoid head-of-line blocking. Lifted
		// to retryable once a DLQ topic is wired up.
		return nil
	}

	for _, pid := range affected {
		if err := c.cache.Delete(ctx, fmt.Sprintf(cache.IdentityKey, pid)); err != nil {
			c.metrics.IncInvalidation(env.EventType, ResultCacheError)
			return err
		}
	}

	if err := c.deduper.Mark(ctx, env.EventID); err != nil {
		c.metrics.IncInvalidation(env.EventType, ResultCacheError)
		return err
	}

	c.metrics.IncInvalidation(env.EventType, ResultOK)
	return nil
}
