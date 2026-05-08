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

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	gswf "github.com/gitscale-platform/gitscale/plane/workflow"
	"github.com/gitscale-platform/gitscale/plane/workflow/appclient"
	"github.com/gitscale-platform/gitscale/plane/workflow/billing"
	"github.com/gitscale-platform/gitscale/plane/workflow/canary"
	"github.com/gitscale-platform/gitscale/plane/workflow/observability"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	// Spec D7 (#62): bootstrap OpenTelemetry tracing before constructing the
	// Temporal client so the interceptor uses the configured TracerProvider.
	// SetupTracing runs from main, not a workflow function, so the
	// network/global-state side effects do not violate Temporal determinism.
	tracingCtx, cancelTracing := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownTracing, err := observability.SetupTracing(tracingCtx, observability.Config{
		ServiceName:      "workflow-worker",
		ServiceNamespace: namespace,
		Environment:      envDefault("GITSCALE_ENV", "dev"),
		OTLPEndpoint:     os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	})
	cancelTracing()
	if err != nil {
		return fmt.Errorf("observability.SetupTracing: %w", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(sctx); err != nil {
			logger.Error("otel shutdown", "err", err)
		}
	}()

	c, err := client.Dial(client.Options{
		HostPort:     host,
		Namespace:    namespace,
		Logger:       sdkLogger{logger},
		Interceptors: observability.TemporalInterceptor(),
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
		Interceptors:                       observability.TemporalWorkerInterceptor(),
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

		// ArchiveDeps wiring (#76, ADR-018 + ADR-019). Opt-in via S3_BUCKET:
		// when set, build the four archive activities (DDL via Postgres
		// archiver, encrypted parquet via Vault transit + S3) and register
		// them through billing.Bundle. When unset, skip — Bundle handles a
		// nil ArchiveDeps and the schedule is not registered.
		var archiveDeps *billing.ArchiveDeps
		var billingConn *grpc.ClientConn
		defer func() {
			if billingConn != nil {
				_ = billingConn.Close()
			}
		}()
		if bucket := os.Getenv("S3_BUCKET"); bucket != "" {
			billingConn, err = dialBillingService(
				os.Getenv("BILLING_SERVICE_ADDR"),
				envBool("WORKER_BILLING_INSECURE", false),
			)
			if err != nil {
				return fmt.Errorf("billing dial: %w", err)
			}
			billingClient := appclient.NewGRPCBillingClient(billingConn)

			vaultClient, err := billing.LoadVaultClientFromEnv()
			if err != nil {
				return fmt.Errorf("vault client: %w", err)
			}
			keys := billing.NewVaultKeyProvider(
				vaultClient,
				envDefault("VAULT_TRANSIT_MOUNT", ""),
				envDefault("VAULT_BILLING_KEY", ""),
			)

			s3Ctx, cancelS3 := context.WithTimeout(context.Background(), 10*time.Second)
			s3client, err := buildS3ClientFromEnv(s3Ctx)
			cancelS3()
			if err != nil {
				return fmt.Errorf("s3 client: %w", err)
			}
			objStore := billing.NewS3ObjectStore(s3client, bucket)

			archiver := billingstore.NewPostgresArchiver(pool)
			detach, err := billing.NewDetachPartitionActivity(archiver)
			if err != nil {
				return fmt.Errorf("detach activity: %w", err)
			}
			drop, err := billing.NewDropPartitionActivity(archiver)
			if err != nil {
				return fmt.Errorf("drop activity: %w", err)
			}
			emit, err := billing.NewEmitArchiveEventActivity(billingClient)
			if err != nil {
				return fmt.Errorf("emit activity: %w", err)
			}
			export, err := billing.NewExportActivity(archiver, objStore, keys, bucket)
			if err != nil {
				return fmt.Errorf("export activity: %w", err)
			}

			// GlueRegisterActivity (#77, ADR-018 §Query path). Reuses the same
			// AWS SDK config as the S3 client; AWS_REGION applies. Database
			// and table names are constants matching terraform/analytics.
			glueCtx, cancelGlue := context.WithTimeout(context.Background(), 10*time.Second)
			glueCfg, err := awsconfig.LoadDefaultConfig(glueCtx,
				awsconfig.WithRegion(envDefault("AWS_REGION", envDefault("S3_REGION", "us-east-1"))),
			)
			cancelGlue()
			if err != nil {
				return fmt.Errorf("aws config (glue): %w", err)
			}
			glueClient := glue.NewFromConfig(glueCfg, func(o *glue.Options) {
				if endpoint := os.Getenv("GLUE_ENDPOINT"); endpoint != "" {
					o.BaseEndpoint = aws.String(endpoint)
				}
			})
			glueRegister, err := billing.NewGlueRegisterActivity(
				glueClient,
				envDefault("GLUE_DATABASE", "gitscale_analytics"),
				envDefault("GLUE_TABLE", "usage_events"),
			)
			if err != nil {
				return fmt.Errorf("glue register activity: %w", err)
			}

			archiveDeps = &billing.ArchiveDeps{
				Detach:       detach,
				Export:       export,
				Emit:         emit,
				GlueRegister: glueRegister,
				Drop:         drop,
			}
			logger.Info("billing archive deps wired",
				"bucket", bucket,
				"billing_addr", os.Getenv("BILLING_SERVICE_ADDR"),
				"glue_database", envDefault("GLUE_DATABASE", "gitscale_analytics"),
				"glue_table", envDefault("GLUE_TABLE", "usage_events"))
		} else {
			logger.Info("S3_BUCKET unset; skipping archive deps + schedule")
		}

		billing.Bundle(partActivity, archiveDeps).Apply(workerRegistrar{w})

		// Outbox TTL expirer (#45, ADR-008): one Expirer per domain, dispatched
		// by a single fan-out workflow on the same task queue.
		expirers := map[store.Domain]*outbox.Expirer{
			store.DomainIdentity:      outbox.NewExpirer(pool, store.DomainIdentity, outbox.ExpirerOptions{}),
			store.DomainRepositories:  outbox.NewExpirer(pool, store.DomainRepositories, outbox.ExpirerOptions{}),
			store.DomainCollaboration: outbox.NewExpirer(pool, store.DomainCollaboration, outbox.ExpirerOptions{}),
			store.DomainCI:            outbox.NewExpirer(pool, store.DomainCI, outbox.ExpirerOptions{}),
			store.DomainBilling:       outbox.NewExpirer(pool, store.DomainBilling, outbox.ExpirerOptions{}),
		}
		outboxttl.Bundle(outboxttl.NewExpireDomainOutboxActivity(expirers)).Apply(workerRegistrar{w})
		logger.Info("outbox TTL expirer registered",
			"workflow", "ExpireOutboxesWorkflow",
			"activity", outboxttl.ActivityNameExpireDomainOutbox)

		// Register / converge the monthly rollover schedule.
		ctxSched, cancelSched := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = billing.EnsureRolloverSchedule(ctxSched, c.ScheduleClient())
		cancelSched()
		if err != nil {
			return fmt.Errorf("billing.EnsureRolloverSchedule: %w", err)
		}
		logger.Info("billing partition-rollover registered",
			"schedule_id", billing.ScheduleID, "cron", billing.CronExpression)

		// Register / converge the monthly archive schedule when archive
		// deps are wired (#76). The schedule targets ArchiveRouterWorkflow
		// which computes (year, month) := workflow.Now − 18mo at fire time.
		if archiveDeps != nil {
			ctxArch, cancelArch := context.WithTimeout(context.Background(), 10*time.Second)
			_, err = billing.EnsureArchiveSchedule(ctxArch, c.ScheduleClient())
			cancelArch()
			if err != nil {
				return fmt.Errorf("billing.EnsureArchiveSchedule: %w", err)
			}
			logger.Info("billing partition-archive registered",
				"schedule_id", billing.ArchiveScheduleID,
				"cron", billing.ArchiveCronExpression)
		}
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

// dialBillingService dials the application-plane billing service. Until
// SPIRE/SPIFFE wiring lands (ADR-010) only WORKER_BILLING_INSECURE=true is
// supported, mirroring the existing IDENTITY_SERVICE_INSECURE convention.
func dialBillingService(addr string, allowInsecure bool) (*grpc.ClientConn, error) {
	if addr == "" {
		return nil, errors.New("BILLING_SERVICE_ADDR is empty")
	}
	if !allowInsecure {
		return nil, errors.New("only WORKER_BILLING_INSECURE=true is supported until SPIRE/SPIFFE wiring lands (ADR-010)")
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// buildS3ClientFromEnv builds an AWS SDK v2 *s3.Client honouring S3_REGION
// (default us-east-1) and the optional S3_ENDPOINT override (path-style for
// minio dev). Credentials come from the default AWS chain.
func buildS3ClientFromEnv(ctx context.Context) (*s3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(envDefault("S3_REGION", "us-east-1")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	}), nil
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
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
