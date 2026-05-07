package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/identity-cache-invalidator"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	kafkago "github.com/segmentio/kafka-go"
)

func main() {
	cfg, err := invalidator.LoadEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	rawStore, err := cache.NewRedisStore(cache.RedisConfig{
		URL:        cfg.RedisURL,
		UseCluster: cfg.RedisUseCluster,
	})
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	cstore := cache.WithNamespace(rawStore, cfg.Env)

	startOffset := kafkago.FirstOffset
	if cfg.KafkaAutoOffsetReset == "latest" {
		startOffset = kafkago.LastOffset
	}
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        cfg.KafkaBootstrapServers,
		Topic:          cfg.KafkaTopic,
		GroupID:        cfg.KafkaGroupID,
		StartOffset:    startOffset,
		MinBytes:       1,
		MaxBytes:       10 << 20, // 10MB
		CommitInterval: 0,        // sync commits
		MaxWait:        5 * time.Second,
	})

	consumer, err := invalidator.New(invalidator.Config{
		Reader:  invalidator.WrapKafkaReader(reader),
		Cache:   cstore,
		Deduper: invalidator.NewDeduper(cstore),
		Metrics: invalidator.NewMetrics(),
	})
	if err != nil {
		log.Fatalf("consumer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Println("shutdown signal; cancelling consumer")
		cancel()
	}()

	if err := consumer.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("run: %v", err)
	}
}
