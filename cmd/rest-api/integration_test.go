//go:build integration

package main_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestRestAPI_BinaryBoots compiles the rest-api binary, points it at a
// fresh testcontainers Postgres, and asserts /healthz answers 200 within
// the boot budget.
func TestRestAPI_BinaryBoots(t *testing.T) {
	ctx := context.Background()
	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("gitscale_test"),
		pgmodule.WithUsername("gs"),
		pgmodule.WithPassword("gs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "rest-api-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"REST_API_ADDR=127.0.0.1:18086",
		"REST_API_INSECURE=true",
		"POSTGRES_DSN="+dsn,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Poll /healthz with a short budget.
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:18086/healthz")
		if err == nil {
			if resp.StatusCode == 200 {
				resp.Body.Close()
				return
			}
			resp.Body.Close()
			lastErr = nil
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("/healthz never returned 200: %v", lastErr)
}
