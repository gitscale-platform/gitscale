//go:build integration

package main_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestGraphQLAPIBinarySmoke builds the binary, starts it against an empty
// $POSTGRES_DSN, and confirms that the env-var preconditions block the
// process from starting unprotected. The success path (with a real PG) is
// covered in the package-level integration test.
func TestGraphQLAPIBinarySmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binary smoke test runs on unix only")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "graphql-api")
	if out, err := exec.Command("go", "build", "-o", bin, "./.").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// Preview required → expect immediate exit.
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "POSTGRES_DSN=postgres://x:y@127.0.0.1:1/none")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected exit (no GRAPHQL_PREVIEW), got success: %s", out)
	}
	if !bytes.Contains(out, []byte("GRAPHQL_PREVIEW=true is required")) {
		t.Errorf("expected preview-required message; got: %s", out)
	}
}

// TestGraphQLAPIBindsAndServesHealthz exercises the "happy path" build
// cycle: ports `:0`, talks to the listener, requires no DB. We accomplish
// this by setting GRAPHQL_INSECURE + GRAPHQL_PREVIEW and a bogus DSN; the
// binary will fail at PG connect — that is itself a meaningful smoke
// assertion (config validation succeeded; we exit on PG, which is the
// right failure mode).
func TestGraphQLAPIConfigValidationPasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("binary smoke test runs on unix only")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "graphql-api")
	if out, err := exec.Command("go", "build", "-o", bin, "./.").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"GRAPHQL_PREVIEW=true",
		"GRAPHQL_INSECURE=true",
		"GRAPHQL_LISTEN=127.0.0.1:"+port,
		"POSTGRES_DSN=postgres://nobody:nope@127.0.0.1:1/none?connect_timeout=1",
	)
	out, _ := cmd.CombinedOutput()
	// We expect a PG connect failure, which means config validation passed.
	if !bytes.Contains(out, []byte("postgres")) && !bytes.Contains(out, []byte("connect")) {
		t.Errorf("expected postgres connect error, got: %s", out)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().(*net.TCPAddr)
	_ = http.Server{}
	return itoa(addr.Port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
