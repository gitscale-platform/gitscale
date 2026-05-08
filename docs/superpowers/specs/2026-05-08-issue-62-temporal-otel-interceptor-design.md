# Spec — Issue #62 OTel interceptor + resource attributes for Temporal worker

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/62
Plane: workflow
Priority: p2 (Wave 0)
ADR-impact: none (operational; spec D7 deferred from #57)

## Problem

`cmd/workflow-worker` runs without distributed tracing. Spec D7 calls for
OTel spans on workflow + activity execution. Without it, partial failures in
multi-activity workflows (e.g. archive, rollover) surface only via Temporal
event history — slow to root-cause and impossible to correlate with calls
into the application plane.

## Goals

1. Install Temporal's OTel interceptor on both `client.Options` and
   `worker.Options` so every workflow + activity attempt becomes a span.
2. Resource attributes: `service.name=workflow-worker`,
   `service.namespace=<TEMPORAL_NAMESPACE>`, `deployment.environment=<env>`.
3. OTLP exporter wired from `OTEL_EXPORTER_OTLP_ENDPOINT`. Missing endpoint
   defaults to a no-op tracer provider so the worker boots in environments
   without an OTel collector.
4. CanaryWorkflow continues to pass; one span per activity attempt is
   asserted by an integration test using an in-memory span recorder.

## Non-goals

- Metrics (OTel metrics is its own deferred item).
- Traces from inside the workflow function (Temporal forbids non-deterministic
  network I/O in workflow goroutines; spans inside activities are the
  contract).
- Sampling configuration (default `ParentBased(AlwaysSample)`; tune later).
- Trace context propagation into the application plane gRPC client (works
  out-of-the-box once the OTel interceptor is registered globally).

## Architecture

### Dependencies

Add to `go.mod`:

- `go.opentelemetry.io/otel`
- `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`
- `go.opentelemetry.io/otel/sdk/trace`
- `go.opentelemetry.io/otel/sdk/resource`
- `go.opentelemetry.io/otel/semconv/v1.26.0`
- `go.temporal.io/sdk/contrib/opentelemetry`

### New package: `plane/workflow/observability`

```go
// Package observability bootstraps OTel for the workflow worker.
package observability

// Config is the env-derived knob set.
type Config struct {
    ServiceName       string // "workflow-worker" — caller fills
    ServiceNamespace  string // TEMPORAL_NAMESPACE
    Environment       string // GITSCALE_ENV ("prod" | "staging" | "dev")
    OTLPEndpoint      string // OTEL_EXPORTER_OTLP_ENDPOINT; "" → no-op
}

// SetupTracing constructs a TracerProvider configured per cfg, registers it
// as the global provider, and returns a shutdown func to flush and close
// the exporter on worker stop. If cfg.OTLPEndpoint == "", returns a no-op
// provider (still global) and a no-op shutdown.
func SetupTracing(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error)

// TemporalInterceptor returns a slice suitable for both
// client.Options.Interceptors and worker.Options.Interceptors, configured
// with the package-level Tracer.
func TemporalInterceptor() []interceptor.ClientInterceptor
```

Implementation:

- `SetupTracing`: build `resource.Resource` with `service.name`,
  `service.namespace`, `deployment.environment`, `service.version` (best-effort
  from runtime/debug ReadBuildInfo). When endpoint is empty, set the
  `noop.NewTracerProvider()` as global. When set, build OTLP gRPC exporter
  with insecure transport (matches existing dev posture) and a batched
  span processor with default options.
- `TemporalInterceptor`: returns the result of
  `temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{Tracer: otel.GetTracerProvider().Tracer("workflow-worker")})`.

### Wiring in `cmd/workflow-worker/main.go`

After env parsing, before constructing the Temporal client:

```go
shutdownTracing, err := observability.SetupTracing(ctx, observability.Config{
    ServiceName:      "workflow-worker",
    ServiceNamespace: namespace,
    Environment:      envDefault("GITSCALE_ENV", "dev"),
    OTLPEndpoint:     os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
})
if err != nil {
    return fmt.Errorf("otel: %w", err)
}
defer func() {
    sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := shutdownTracing(sctx); err != nil {
        logger.Error("otel shutdown", "err", err)
    }
}()

interceptors := observability.TemporalInterceptor()

c, err := client.Dial(client.Options{
    HostPort:     host,
    Namespace:    namespace,
    Interceptors: interceptors,
    Logger:       logger,
})
// ...
w := worker.New(c, gswf.QueueBillingMaintenance, worker.Options{
    Interceptors: interceptors,
    // ...
})
```

The same `interceptors` slice is fed to both Options to satisfy Temporal's
contract that the client's interceptors are present on the worker too (so
spans created during activity scheduling propagate into activity execution).

### Verification test

`plane/workflow/observability/setup_test.go`:

- Stand up an in-memory span recorder
  (`tracetest.NewSpanRecorder` from `go.opentelemetry.io/otel/sdk/trace/tracetest`).
- Build a TracerProvider with the recorder and register globally.
- Run `CanaryWorkflow` against a `testsuite.TestWorkflowEnvironment` with
  `TemporalInterceptor()` registered.
- Assert: at least one span exists with name matching `RunActivity:*`
  and resource attribute `service.name=workflow-worker`.

(The existing `plane/workflow/canary` package owns CanaryWorkflow; reuse it.)

### Behaviour when endpoint is unset

- Global TracerProvider is a no-op.
- Temporal interceptor is still installed (cheap; spans go to no-op).
- `gh pr checks` for the existing CanaryWorkflow tests must remain green —
  i.e. no behavioural drift other than the interceptor presence.

## Test plan

| Layer | Test |
|---|---|
| Unit | `SetupTracing` with empty endpoint returns no-op shutdown + no error |
| Unit | `SetupTracing` with bogus endpoint returns config but lazy export errors are non-fatal |
| Integration (in-memory recorder) | CanaryWorkflow run produces spans with the right resource attributes; one span per activity attempt |
| Existing | `plane/workflow/...` and `cmd/workflow-worker/...` tests pass unchanged |

## Acceptance checklist

- [ ] `plane/workflow/observability/{setup.go, setup_test.go}` implemented
- [ ] `cmd/workflow-worker/main.go` wires interceptors on client + worker
- [ ] Resource attributes (`service.name`, `service.namespace`,
      `deployment.environment`) present on emitted spans
- [ ] OTLP endpoint configurable via env; default is no-op
- [ ] CanaryWorkflow integration test asserts span emission
- [ ] PR description references spec D7

## Open questions

None.

## References

- Spec D7 (deferred from #57)
- `plane/workflow/canary/` for the reference CanaryWorkflow
- `cmd/workflow-worker/main.go` for env-handling conventions
- Temporal interceptor docs: https://github.com/temporalio/sdk-go/tree/master/contrib/opentelemetry
