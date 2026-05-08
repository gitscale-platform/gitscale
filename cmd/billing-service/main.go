// billing-service is the application-plane gRPC binary for the billing
// domain (ADR-019). State-mutating RPCs flow through this process exclusively;
// the workflow plane reaches it via plane/workflow/appclient.
//
// mTLS via SPIRE/SPIFFE (ADR-010) is the production posture. Until SPIRE
// rolls, the binary boots with insecure transport credentials when
// BILLING_SERVICE_INSECURE=true.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	billingv1 "github.com/gitscale-platform/gitscale/internal/proto/gitscale/billing/v1"
	"github.com/gitscale-platform/gitscale/plane/application/billing"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

type config struct {
	Addr        string
	PostgresDSN string
	Insecure    bool
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:     getenv("BILLING_SERVICE_ADDR", ":8087"),
		Insecure: getenvBool("BILLING_SERVICE_INSECURE", false),
	}
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	if cfg.PostgresDSN == "" {
		return cfg, errors.New("POSTGRES_DSN is required")
	}
	if !cfg.Insecure {
		return cfg, errors.New("only BILLING_SERVICE_INSECURE=true is supported until SPIRE/SPIFFE wiring lands (ADR-010)")
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

	store := storepg.New(pool)
	svc := billing.NewPostgresService(store)
	srv := grpc.NewServer()
	billingv1.RegisterBillingServiceServer(srv, billing.NewGRPCServer(svc))

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Addr, err)
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		log.Println("shutdown signal; stopping gRPC server")
		srv.GracefulStop()
	}()

	log.Printf("billing-service listening on %s (insecure=%v)", cfg.Addr, cfg.Insecure)
	if err := srv.Serve(lis); err != nil {
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
