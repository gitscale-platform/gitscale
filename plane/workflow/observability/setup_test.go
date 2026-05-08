package observability_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/observability"
)

// TestSetupTracing_EmptyEndpointIsNoop verifies that omitting the OTLP
// endpoint installs a no-op TracerProvider and returns a no-op shutdown.
// This is the default behaviour when GITSCALE_ENV=dev and no collector is
// configured — the worker must boot cleanly.
func TestSetupTracing_EmptyEndpointIsNoop(t *testing.T) {
	shutdown, err := observability.SetupTracing(context.Background(), observability.Config{
		ServiceName:      "test",
		ServiceNamespace: "ns",
		Environment:      "dev",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned: %v", err)
	}
}

// TestSetupTracing_BogusEndpointDoesNotPanic exercises the OTLP gRPC path.
// The exporter creates a lazy connection, so an unreachable endpoint must
// not fail SetupTracing. Shutdown best-effort may error but must not panic.
func TestSetupTracing_BogusEndpointDoesNotPanic(t *testing.T) {
	shutdown, err := observability.SetupTracing(context.Background(), observability.Config{
		ServiceName:      "test",
		ServiceNamespace: "ns",
		Environment:      "dev",
		OTLPEndpoint:     "127.0.0.1:1", // unreachable; OTLP gRPC connects lazily
	})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	// Best-effort shutdown — may error against an unreachable endpoint but
	// must return rather than panic.
	_ = shutdown(context.Background())
}

// TestTemporalInterceptor_NonEmpty asserts the interceptor slice contains
// exactly one entry, which is what client.Options + worker.Options expect.
func TestTemporalInterceptor_NonEmpty(t *testing.T) {
	if got := observability.TemporalInterceptor(); len(got) != 1 {
		t.Fatalf("expected 1 interceptor, got %d", len(got))
	}
}
