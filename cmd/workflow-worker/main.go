// Command workflow-worker is the binary that hosts GitScale's Temporal
// workflows and activities (ADR-003). One process registers one or more
// task queues; today only QueueBillingMaintenance has live work (canary +
// future #18-rollover), the other queues are reserved.
//
// Required env vars:
//
//	TEMPORAL_NAMESPACE  — gitscale-{prod,staging,dev}; fail-fast if unset
//	TEMPORAL_HOST       — host:port for the Temporal frontend (default localhost:7233)
//	REDIS_ADDR          — host:port for the Redis cache (canary reads workflow:health)
//
// Optional env vars (worker tunables, spec D8):
//
//	WORKER_MAX_CONCURRENT_ACTIVITIES  (default 100)
//	WORKER_WORKFLOW_POLLERS           (default 4)
//	WORKER_ACTIVITY_POLLERS           (default 4)
//	WORKER_STOP_TIMEOUT               (default 30s)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"github.com/gitscale-platform/gitscale/plane/workflow/billing"
	"github.com/gitscale-platform/gitscale/plane/workflow/canary"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("worker exited with error", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	namespace := os.Getenv("TEMPORAL_NAMESPACE")
	if namespace == "" {
		return errors.New("TEMPORAL_NAMESPACE is required (e.g. gitscale-dev)")
	}
	host := envDefault("TEMPORAL_HOST", "localhost:7233")
	redisAddr := envDefault("REDIS_ADDR", "localhost:6379")

	stopTimeout := envDuration("WORKER_STOP_TIMEOUT", 30*time.Second)
	maxActivities := envInt("WORKER_MAX_CONCURRENT_ACTIVITIES", 100)
	workflowPollers := envInt("WORKER_WORKFLOW_POLLERS", 4)
	activityPollers := envInt("WORKER_ACTIVITY_POLLERS", 4)

	logger.Info("connecting to Temporal",
		"namespace", namespace, "host", host)

	c, err := client.Dial(client.Options{
		HostPort:  host,
		Namespace: namespace,
		Logger:    sdkLogger{logger},
	})
	if err != nil {
		return fmt.Errorf("temporal client.Dial: %w", err)
	}
	defer c.Close()

	cacheStore, err := cache.NewRedisStore(cache.RedisConfig{URL: redisAddr})
	if err != nil {
		return fmt.Errorf("cache.NewRedisStore: %w", err)
	}

	w := worker.New(c, gswf.QueueBillingMaintenance, worker.Options{
		MaxConcurrentActivityExecutionSize: maxActivities,
		MaxConcurrentWorkflowTaskPollers:   workflowPollers,
		MaxConcurrentActivityTaskPollers:   activityPollers,
		WorkerStopTimeout:                  stopTimeout,
	})

	canary.Bundle(cacheStore).Apply(workerRegistrar{w})

	// billing.Bundle is opt-in: only wire when POSTGRES_URL is set so dev
	// boots succeed without a postgres dependency. In prod the env is
	// always populated by the deploy template.
	pgURL := os.Getenv("POSTGRES_URL")
	if pgURL != "" {
		ctxBoot, cancelBoot := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err := pgxpool.New(ctxBoot, pgURL)
		cancelBoot()
		if err != nil {
			return fmt.Errorf("pgxpool.New: %w", err)
		}
		defer pool.Close()

		partActivity, err := billing.NewCreatePartitionActivity(billingstore.NewPostgresPartitioner(pool))
		if err != nil {
			return fmt.Errorf("billing.NewCreatePartitionActivity: %w", err)
		}
		billing.Bundle(partActivity, nil).Apply(workerRegistrar{w})

		// Register / converge the monthly rollover schedule.
		ctxSched, cancelSched := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = billing.EnsureRolloverSchedule(ctxSched, c.ScheduleClient())
		cancelSched()
		if err != nil {
			return fmt.Errorf("billing.EnsureRolloverSchedule: %w", err)
		}
		logger.Info("billing partition-rollover registered",
			"schedule_id", billing.ScheduleID, "cron", billing.CronExpression)
	} else {
		logger.Info("POSTGRES_URL unset; skipping billing.Bundle + rollover schedule")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("worker starting",
		"queue", gswf.QueueBillingMaintenance,
		"max_activities", maxActivities,
		"workflow_pollers", workflowPollers,
		"activity_pollers", activityPollers,
		"stop_timeout", stopTimeout)

	if err := w.Start(); err != nil {
		return fmt.Errorf("worker.Start: %w", err)
	}

	<-ctx.Done()
	logger.Info("shutdown signal received; stopping worker")
	w.Stop()
	return nil
}

// workerRegistrar adapts *worker.Worker to gswf.Registrar so a Bundle can
// register against it without the workflow package depending on the SDK.
type workerRegistrar struct{ w worker.Worker }

func (r workerRegistrar) RegisterWorkflow(wf any) { r.w.RegisterWorkflow(wf) }

// RegisterActivity recognises NamedActivity (registers with an explicit name)
// and falls through to the SDK's default name derivation for any other type.
func (r workerRegistrar) RegisterActivity(a any) {
	if named, ok := a.(gswf.NamedActivity); ok {
		r.w.RegisterActivityWithOptions(named.Activity, activity.RegisterOptions{Name: named.Name})
		return
	}
	r.w.RegisterActivity(a)
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// sdkLogger adapts *slog.Logger to the temporal SDK's log.Logger interface.
type sdkLogger struct{ l *slog.Logger }

func (s sdkLogger) Debug(msg string, kv ...any) { s.l.Debug(msg, kv...) }
func (s sdkLogger) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s sdkLogger) Warn(msg string, kv ...any)  { s.l.Warn(msg, kv...) }
func (s sdkLogger) Error(msg string, kv ...any) { s.l.Error(msg, kv...) }
