// Package wiring constructs one OutboxConsumer per domain from a shared DB
// pool and a shared Kafka producer. Each consumer is independent; a failure
// in one domain does not affect the others (ADR-008).
package wiring

import (
	"context"
	"fmt"

	kafkadata "github.com/gitscale-platform/gitscale/plane/data/kafka"
	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DomainConfig maps one domain's table and topic names to a consumer config.
type DomainConfig struct {
	Domain store.Domain
	Table  string
	Topic  string
}

// AllDomains is the canonical list of the five GitScale schema domains and
// their corresponding outbox tables and Kafka topics.
var AllDomains = []DomainConfig{
	{
		Domain: store.DomainIdentity,
		Table:  store.DomainIdentity.OutboxTable(),
		Topic:  kafkadata.TopicIdentityEvents,
	},
	{
		Domain: store.DomainRepositories,
		Table:  store.DomainRepositories.OutboxTable(),
		Topic:  kafkadata.TopicRepositoriesEvents,
	},
	{
		Domain: store.DomainCollaboration,
		Table:  store.DomainCollaboration.OutboxTable(),
		Topic:  kafkadata.TopicCollaborationEvents,
	},
	{
		Domain: store.DomainCI,
		Table:  store.DomainCI.OutboxTable(),
		Topic:  kafkadata.TopicCIEvents,
	},
	{
		Domain: store.DomainBilling,
		Table:  store.DomainBilling.OutboxTable(),
		Topic:  kafkadata.TopicBillingEvents,
	},
}

// StartAll creates one OutboxConsumer per domain, starts each in its own
// goroutine, and returns a cancel function that stops all consumers.
//
// Each consumer applies env-var defaults (OUTBOX_POLL_INTERVAL_MS,
// OUTBOX_PUBLISH_TIMEOUT_MS, OUTBOX_BATCH_SIZE) via Config.ApplyEnvDefaults.
//
// The returned errCh receives the first non-context-cancellation error from
// any consumer; callers may ignore it for fire-and-forget usage.
func StartAll(
	ctx context.Context,
	db *pgxpool.Pool,
	prod outbox.KafkaProducer,
	domains []DomainConfig,
) (cancel context.CancelFunc, errCh <-chan error, err error) {
	runCtx, cancelFn := context.WithCancel(ctx)

	ch := make(chan error, len(domains))
	consumers := make([]*outbox.OutboxConsumer, 0, len(domains))

	for _, d := range domains {
		cfg := outbox.Config{
			Domain:   string(d.Domain),
			Table:    d.Table,
			Topic:    d.Topic,
			DB:       db,
			Producer: prod,
		}
		if applyErr := cfg.ApplyEnvDefaults(); applyErr != nil {
			cancelFn()
			return nil, nil, fmt.Errorf("wiring: domain %s: %w", d.Domain, applyErr)
		}
		c, newErr := outbox.NewOutboxConsumer(cfg)
		if newErr != nil {
			cancelFn()
			return nil, nil, fmt.Errorf("wiring: domain %s: %w", d.Domain, newErr)
		}
		consumers = append(consumers, c)
	}

	for _, c := range consumers {
		c := c
		go func() {
			if runErr := c.Run(runCtx); runErr != nil && runErr != context.Canceled {
				ch <- runErr
			}
		}()
	}

	return cancelFn, ch, nil
}
