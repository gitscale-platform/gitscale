package observability

import (
	"context"
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// Config carries the env-derived knobs for tracing setup. All fields are
// supplied by cmd/workflow-worker; nothing is read from the environment in
// this package so unit tests can drive it deterministically.
type Config struct {
	ServiceName      string // e.g. "workflow-worker"
	ServiceNamespace string // typically TEMPORAL_NAMESPACE
	Environment      string // GITSCALE_ENV ("prod" | "staging" | "dev")
	OTLPEndpoint     string // OTEL_EXPORTER_OTLP_ENDPOINT; empty → no-op
}

// noopShutdown is the placeholder returned when tracing is disabled.
func noopShutdown(context.Context) error { return nil }

// SetupTracing installs the global TracerProvider per cfg. When
// cfg.OTLPEndpoint is empty, a no-op provider is registered and the returned
// shutdown is a no-op so the worker boots without an OTel collector.
//
// Caller is expected to invoke the returned shutdown during worker stop with
// a bounded-context (typically 5s) to flush pending spans.
func SetupTracing(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.OTLPEndpoint == "" {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return noopShutdown, nil
	}
	res, err := buildResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	exporter, err := otlptrace.New(ctx, otlptracegrpc.NewClient(
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	))
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	return func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("tracer provider shutdown: %w", err)
		}
		if err := exporter.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("otlp exporter shutdown: %w", err)
		}
		return nil
	}, nil
}

// buildResource composes the resource attributes prescribed by spec D7:
// service.name, service.namespace, deployment.environment, plus a best-effort
// service.version derived from build info.
func buildResource(cfg Config) (*resource.Resource, error) {
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version = info.Main.Version
	}
	return resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceNamespace(cfg.ServiceNamespace),
			semconv.DeploymentEnvironment(cfg.Environment),
			semconv.ServiceVersion(version),
		),
	)
}

// TemporalInterceptor returns the interceptor slice consumed by both
// client.Options.Interceptors and worker.Options.Interceptors. It must be
// fed to both so spans created during activity scheduling propagate into
// activity execution (Temporal contract).
func TemporalInterceptor() []interceptor.ClientInterceptor {
	intr, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{
		Tracer: otel.GetTracerProvider().Tracer("workflow-worker"),
	})
	if err != nil {
		// NewTracingInterceptor only errors on programmer mistake (e.g. nil
		// tracer); the global provider always returns a real tracer here.
		// Returning empty rather than panicking keeps the worker bootable
		// even in the unlikely event the contract changes upstream.
		return nil
	}
	return []interceptor.ClientInterceptor{intr}
}
