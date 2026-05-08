// Package observability bootstraps OpenTelemetry tracing for the workflow
// worker (issue #62, spec D7).
//
// Public API:
//
//   SetupTracing(ctx, Config) registers a global TracerProvider and returns
//   a shutdown func that flushes pending spans on worker stop. When
//   Config.OTLPEndpoint is empty, a no-op TracerProvider is registered and
//   the returned shutdown is a no-op — the worker boots in environments
//   without an OTel collector.
//
//   TemporalInterceptor() returns the slice consumed by both
//   client.Options.Interceptors and worker.Options.Interceptors so spans
//   span the boundary between scheduling and execution.
//
// Determinism note: SetupTracing is invoked from cmd/workflow-worker.main —
// not from inside any workflow function — so the time/network/global-state
// side effects here do not violate Temporal determinism rules (ADR-003).
package observability
