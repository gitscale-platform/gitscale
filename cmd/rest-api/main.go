// rest-api is the application-plane HTTP/JSON edge for identity and
// repositories (ADR-019). It hosts /v1/* routes and a /healthz probe.
//
// mTLS via SPIRE/SPIFFE (ADR-010) lands at the edge plane, not here — this
// binary trusts that requests have already passed Envoy's identity filter
// when deployed behind the standard gateway. For stand-alone local runs
// REST_API_INSECURE=true is required so the operator acknowledges the gap.
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

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	Addr              string
	PostgresDSN       string
	Insecure          bool
	AgentCapacity     float64
	AgentRefillPerSec float64
	HumanCapacity     float64
	HumanRefillPerSec float64
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:              getenv("REST_API_ADDR", ":8086"),
		Insecure:          getenvBool("REST_API_INSECURE", false),
		AgentCapacity:     getenvFloat("RATELIMIT_AGENT_CAPACITY", 200),
		AgentRefillPerSec: getenvFloat("RATELIMIT_AGENT_REFILL_PER_SEC", 200),
		HumanCapacity:     getenvFloat("RATELIMIT_HUMAN_CAPACITY", 20),
		HumanRefillPerSec: getenvFloat("RATELIMIT_HUMAN_REFILL_PER_SEC", 20),
	}
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if cfg.PostgresDSN == "" {
		return cfg, errors.New("POSTGRES_DSN is required")
	}
	if !cfg.Insecure {
		return cfg, errors.New("only REST_API_INSECURE=true is supported until edge-plane SPIRE/SPIFFE wiring lands (ADR-010)")
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

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := pool.Ping(pingCtx); err != nil {
		pingCancel()
		log.Fatalf("postgres ping: %v", err)
	}
	pingCancel()

	mds := storepg.New(pool)
	identitySvc := identity.NewPostgresService(mds)
	reposSvc := repositories.NewService(mds)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// store.IdentityReader satisfies the methods restapi.NewIdentityResolver
	// needs. The reader is used outside any transaction, which is correct
	// for token resolution.
	router := restapi.NewRouter(restapi.Deps{
		Identity:     identitySvc,
		Repositories: reposSvc,
		Resolver:     restapi.NewIdentityResolver(mds.Identity()),
		Limiter:      ratelimit.NewMemoryLimiter(nil),
		RateConfig: restapi.RateConfig{
			AgentCapacity:     cfg.AgentCapacity,
			AgentRefillPerSec: cfg.AgentRefillPerSec,
			HumanCapacity:     cfg.HumanCapacity,
			HumanRefillPerSec: cfg.HumanRefillPerSec,
		},
		Logger: logger,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
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

	logger.Info("rest-api listening", slog.String("addr", cfg.Addr), slog.Bool("insecure", cfg.Insecure))
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
