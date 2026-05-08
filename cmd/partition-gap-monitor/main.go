// partition-gap-monitor exposes /metrics with the partition gap gauges.
//
// It runs in production as a sidecar / lightweight pod scraped by Prometheus.
// Tied to the alert rule deploy/alerts/billing_partition_gap.yaml and
// runbook docs/runbooks/billing-partition-gap.md.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("POSTGRES_DSN is required")
	}
	addr := getenv("PARTITION_GAP_MONITOR_ADDR", ":9100")
	intervalStr := getenv("PARTITION_GAP_MONITOR_INTERVAL", "60s")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		log.Fatalf("invalid PARTITION_GAP_MONITOR_INTERVAL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	metric := billing.NewPartitionGapMetric(pool, reg)
	// Refresh once up front so /metrics has a value before the scraper hits.
	if err := metric.Refresh(ctx); err != nil {
		log.Printf("warning: initial refresh: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := metric.Refresh(ctx); err != nil {
					log.Printf("refresh: %v", err)
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
		shutdownCtx, cleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanup()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("partition-gap-monitor listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
