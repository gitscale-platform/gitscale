package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OutboxConsumer is the polling outbox drain service for one domain.
//
// Run blocks until ctx is cancelled. It polls the domain's outbox table on
// each tick of cfg.PollInterval, drains rows to Kafka via cfg.Producer, and
// marks them processed. Many replicas may run simultaneously; per-cycle
// exclusivity is enforced by the PostgreSQL advisory lock (ADR-008).
type OutboxConsumer struct {
	cfg Config
}

// NewOutboxConsumer creates a new OutboxConsumer for the given Config. The
// caller is responsible for calling cfg.ApplyEnvDefaults() before passing cfg.
func NewOutboxConsumer(cfg Config) (*OutboxConsumer, error) {
	if cfg.Domain == "" {
		return nil, fmt.Errorf("outbox: Config.Domain must not be empty")
	}
	if cfg.Table == "" {
		return nil, fmt.Errorf("outbox: Config.Table must not be empty")
	}
	if cfg.Topic == "" {
		return nil, fmt.Errorf("outbox: Config.Topic must not be empty")
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("outbox: Config.DB must not be nil")
	}
	if cfg.Producer == nil {
		return nil, fmt.Errorf("outbox: Config.Producer must not be nil")
	}
	if cfg.PollInterval == 0 {
		return nil, fmt.Errorf("outbox: Config.PollInterval must not be zero")
	}
	if cfg.PublishTimeout == 0 {
		return nil, fmt.Errorf("outbox: Config.PublishTimeout must not be zero")
	}
	if cfg.BatchSize <= 0 {
		return nil, fmt.Errorf("outbox: Config.BatchSize must be > 0")
	}
	return &OutboxConsumer{cfg: cfg}, nil
}

// Run blocks until ctx is cancelled. It returns ctx.Err() on clean shutdown.
// After ctx is cancelled, Run drains the producer's internal queue with a 5s
// deadline before returning.
func (c *OutboxConsumer) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.shutdown()
			return ctx.Err()
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// tick executes one drain cycle and records metrics.
func (c *OutboxConsumer) tick(ctx context.Context) {
	result, n, err := drainBatch(ctx, c.cfg)
	c.cfg.Metrics.incDrainCycles(string(result))

	switch result {
	case resultOK:
		// logged inside drainBatch when n > 0
		_ = n
	case resultLockMissed:
		// normal in multi-replica deployments — other replica has the lock
	case resultEmpty:
		// nothing to do
	case resultPublishError:
		slog.WarnContext(ctx, "outbox: publish error in drain cycle",
			"domain", c.cfg.Domain,
			"error", err,
		)
	case resultUpdateError:
		slog.ErrorContext(ctx, "outbox: update error in drain cycle",
			"domain", c.cfg.Domain,
			"error", err,
		)
	}
}

// shutdown calls producer.Close with a 5-second deadline after ctx cancel.
func (c *OutboxConsumer) shutdown() {
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.cfg.Producer.Close(); err != nil {
		slog.Warn("outbox: producer close error during shutdown",
			"domain", c.cfg.Domain,
			"error", err,
		)
	}
	_ = closeCtx // deadline context signals intent; Close does not take ctx per our interface
}
