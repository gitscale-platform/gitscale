// mcp-server is the application-plane MCP HTTP server (#112). It boots
// against the same metadata/store stack as cmd/rest-api and exposes
// the seven canonical tools.
//
// Production deployment expects the edge plane (Envoy + WASM, ADR-010)
// to terminate mTLS and stamp JWT-SVID; this binary trusts that work
// has happened at the edge. Stand-alone runs require
// MCP_SERVER_INSECURE=true, mirroring cmd/rest-api.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/mcp"
	"github.com/gitscale-platform/gitscale/plane/application/mcp/cirunclient"
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
	ProtocolVersion   string
	ServerName        string
	ServerVersion     string
	SessionHMACSecret []byte
	AgentCapacity     float64
	AgentRefillPerSec float64
	HumanCapacity     float64
	HumanRefillPerSec float64
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:              getenv("MCP_LISTEN", ":8087"),
		Insecure:          getenvBool("MCP_SERVER_INSECURE", false),
		ProtocolVersion:   getenv("MCP_PROTOCOL_VERSION", mcp.DeferredDefaultProtocolVersion),
		ServerName:        getenv("MCP_SERVER_NAME", "gitscale-mcp"),
		ServerVersion:     getenv("MCP_SERVER_VERSION", "0.1.0"),
		AgentCapacity:     getenvFloat("MCP_RATELIMIT_AGENT_CAPACITY", 200),
		AgentRefillPerSec: getenvFloat("MCP_RATELIMIT_AGENT_REFILL_PER_SEC", 200),
		HumanCapacity:     getenvFloat("MCP_RATELIMIT_HUMAN_CAPACITY", 20),
		HumanRefillPerSec: getenvFloat("MCP_RATELIMIT_HUMAN_REFILL_PER_SEC", 20),
	}
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if cfg.PostgresDSN == "" {
		return cfg, errors.New("POSTGRES_DSN is required")
	}
	secret := os.Getenv("MCP_SESSION_HMAC_SECRET")
	if len(secret) < mcp.MinSessionHMACSecretBytes {
		return cfg, fmt.Errorf("MCP_SESSION_HMAC_SECRET must be at least %d bytes", mcp.MinSessionHMACSecretBytes)
	}
	cfg.SessionHMACSecret = []byte(secret)
	if !cfg.Insecure {
		return cfg, errors.New("only MCP_SERVER_INSECURE=true is supported until edge-plane SPIRE/SPIFFE wiring lands (ADR-010)")
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
	limiter := ratelimit.NewMemoryLimiter(nil)
	resolver := restapi.NewIdentityResolver(mds.Identity())

	restRouter := restapi.NewRouter(restapi.Deps{
		Identity:     identitySvc,
		Repositories: reposSvc,
		Resolver:     resolver,
		Limiter:      limiter,
		RateConfig: restapi.RateConfig{
			AgentCapacity:     cfg.AgentCapacity,
			AgentRefillPerSec: cfg.AgentRefillPerSec,
			HumanCapacity:     cfg.HumanCapacity,
			HumanRefillPerSec: cfg.HumanRefillPerSec,
		},
		Logger: logger,
	})

	srv, err := mcp.NewServer(mcp.Config{
		ProtocolVersion:   cfg.ProtocolVersion,
		ServerName:        cfg.ServerName,
		ServerVersion:     cfg.ServerVersion,
		SessionHMACSecret: cfg.SessionHMACSecret,
		RateConfig: restapi.RateConfig{
			AgentCapacity:     cfg.AgentCapacity,
			AgentRefillPerSec: cfg.AgentRefillPerSec,
			HumanCapacity:     cfg.HumanCapacity,
			HumanRefillPerSec: cfg.HumanRefillPerSec,
		},
	}, mcp.Deps{
		Identity:     identitySvc,
		Repositories: reposSvc,
		Resolver:     resolver,
		Limiter:      limiter,
		// BlobReader / OrgPolicy: production wiring lands when the
		// Gitaly-backed BlobReader and the metadata-resolver from
		// plane/git ship; until then the agents_md_get tool returns
		// not_implemented because Deps.BlobReader is nil.
		CIRunClient: cirunclient.NilClient{},
		RESTHandler: restRouter,
		Logger:      logger,
	})
	if err != nil {
		log.Fatalf("mcp.NewServer: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("mcp-server listen", slog.String("addr", cfg.Addr))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	<-stop
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 5*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
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
		return def
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
		return def
	}
	return f
}
