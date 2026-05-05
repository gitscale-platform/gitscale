// Command outbox-consumer is the binary that drains all five PostgreSQL outbox
// tables and publishes events to Kafka (ADR-008).
//
// Configuration is entirely via environment variables; see Config in
// plane/data/outbox/config.go and the list below.
//
// Required env vars:
//
//	KAFKA_BOOTSTRAP_SERVERS  — comma-separated broker addresses
//	DATABASE_URL             — PostgreSQL DSN (pgx-compatible)
//
// Optional env vars (see outbox.Config):
//
//	OUTBOX_POLL_INTERVAL_MS    (default 1000)
//	OUTBOX_PUBLISH_TIMEOUT_MS  (default 5000)
//	OUTBOX_BATCH_SIZE          (default 100)
//	KAFKA_CLIENT_ID            (default "gitscale-outbox-<hostname>")
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/outbox/wiring"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootstrapServers := requireEnv("KAFKA_BOOTSTRAP_SERVERS")
	databaseURL := requireEnv("DATABASE_URL")

	clientID := os.Getenv("KAFKA_CLIENT_ID")
	if clientID == "" {
		hostname, _ := os.Hostname()
		clientID = "gitscale-outbox-" + hostname
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("outbox-consumer: pgxpool.New", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	prod, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
		BootstrapServers: bootstrapServers,
		ClientID:         clientID,
	})
	if err != nil {
		slog.Error("outbox-consumer: NewKafkaProducer", "error", err)
		os.Exit(1)
	}

	cancelFn, errCh, err := wiring.StartAll(ctx, pool, prod, wiring.AllDomains)
	if err != nil {
		slog.Error("outbox-consumer: StartAll", "error", err)
		os.Exit(1)
	}
	defer cancelFn()

	slog.Info("outbox-consumer: started", "domains", len(wiring.AllDomains))

	select {
	case <-ctx.Done():
		slog.Info("outbox-consumer: shutdown signal received")
	case err := <-errCh:
		slog.Error("outbox-consumer: consumer error", "error", err)
		os.Exit(1)
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("outbox-consumer: required env var not set", "var", key)
		os.Exit(1)
	}
	return v
}
