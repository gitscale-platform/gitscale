// graphql-api is the application-plane GraphQL endpoint for issue #113.
// It mounts /graphql, /graphql/persisted/register, /graphql/persisted/{hash}
// and a /graphql/healthz probe.
//
// Phase 1 ships behind GRAPHQL_PREVIEW=true. The flag is acknowledged at
// startup so the operator opts into the preview surface explicitly; Phase
// 2 GA flips the gate without code changes (ADR-017 swap-surface friendly).
//
// Two pgxpool connections are wired by default — DATABASE_URL_PRIMARY and
// DATABASE_URL_READER — so the follower-read default routes ad-hoc queries
// to the replica while @liveRead and mutations land on Primary. If only
// DATABASE_URL is provided, both pools point at it (single-node dev).
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	gqlplane "github.com/gitscale-platform/gitscale/plane/application/graphql"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	gqlmw "github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/resolvers"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	Addr              string
	PrimaryDSN        string
	ReaderDSN         string
	Preview           bool
	Insecure          bool
	AgentCapacity     float64
	AgentRefillPerSec float64
	HumanCapacity     float64
	HumanRefillPerSec float64
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:              getenv("GRAPHQL_LISTEN", ":8087"),
		Preview:           getenvBool("GRAPHQL_PREVIEW", false),
		Insecure:          getenvBool("GRAPHQL_INSECURE", false),
		AgentCapacity:     getenvFloat("RATELIMIT_AGENT_CAPACITY", 5000),
		AgentRefillPerSec: getenvFloat("RATELIMIT_AGENT_REFILL_PER_SEC", 500),
		HumanCapacity:     getenvFloat("RATELIMIT_HUMAN_CAPACITY", 1000),
		HumanRefillPerSec: getenvFloat("RATELIMIT_HUMAN_REFILL_PER_SEC", 100),
	}
	cfg.PrimaryDSN = getenv("DATABASE_URL_PRIMARY", os.Getenv("POSTGRES_DSN"))
	cfg.ReaderDSN = getenv("DATABASE_URL_READER", cfg.PrimaryDSN)
	if cfg.PrimaryDSN == "" {
		return cfg, errors.New("DATABASE_URL_PRIMARY (or POSTGRES_DSN) is required")
	}
	if !cfg.Preview {
		return cfg, errors.New("GRAPHQL_PREVIEW=true is required while Phase 2 GA gating is in flight (issue #113)")
	}
	if !cfg.Insecure {
		return cfg, errors.New("only GRAPHQL_INSECURE=true is supported until edge-plane SPIRE/SPIFFE wiring lands (ADR-010)")
	}
	return cfg, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	primary, err := pgxpool.New(ctx, cfg.PrimaryDSN)
	if err != nil {
		log.Fatalf("postgres primary: %v", err)
	}
	defer primary.Close()

	reader := primary
	if cfg.ReaderDSN != cfg.PrimaryDSN {
		reader, err = pgxpool.New(ctx, cfg.ReaderDSN)
		if err != nil {
			log.Fatalf("postgres reader: %v", err)
		}
		defer reader.Close()
	}

	pingCtx, pcancel := context.WithTimeout(ctx, 5*time.Second)
	if err := primary.Ping(pingCtx); err != nil {
		pcancel()
		log.Fatalf("primary ping: %v", err)
	}
	if err := reader.Ping(pingCtx); err != nil {
		pcancel()
		log.Fatalf("reader ping: %v", err)
	}
	pcancel()

	primaryStore := storepg.New(primary)
	readerStore := storepg.New(reader)

	identitySvc := identity.NewPostgresService(primaryStore)
	reposSvc := repositories.NewService(primaryStore)
	pStore := persisted.NewCachedStore(persisted.NewPostgresStore(primary), cache.NewMemoryStore(nil))
	limiter := ratelimit.NewMemoryLimiter(nil)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	deps := gqlplane.Deps{
		Pools: gqlmw.Pools{
			Reader:  readerStore,
			Primary: primaryStore,
		},
		Resolver:  restapi.NewIdentityResolver(primaryStore.Identity()),
		Limiter:   limiter,
		Analyzer:  cost.New(cost.DefaultLimits(), cost.DefaultFieldWeights()),
		Persisted: pStore,
		Resolvers: resolvers.Deps{
			Identity:     identitySvc,
			Repositories: reposSvc,
			SVID:         resolvers.AlwaysVerifiedSVID{}, // ADR-010 wiring lands at the edge plane
		},
		Bucket: gqlmw.BucketParams{
			AgentCapacity:     cfg.AgentCapacity,
			AgentRefillPerSec: cfg.AgentRefillPerSec,
			HumanCapacity:     cfg.HumanCapacity,
			HumanRefillPerSec: cfg.HumanRefillPerSec,
		},
		Logger: logger,
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           gqlplane.NewHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("graphql-api listening",
		slog.String("addr", cfg.Addr),
		slog.Bool("preview", cfg.Preview),
		slog.Bool("insecure", cfg.Insecure),
	)
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

func getenvBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("env %s: %s is not a bool: %v", k, v, err)
	}
	return b
}

func getenvFloat(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("env %s: %s is not a float: %v", k, v, err)
	}
	return f
}
