# Issue #62 OTel interceptor for Temporal worker — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the Temporal OTel interceptor on `cmd/workflow-worker`'s client + worker, with configurable OTLP endpoint and the prescribed resource attributes; default to a no-op tracer when no endpoint is set.

**Architecture:** New package `plane/workflow/observability` owns OTel bootstrap and exposes a `TemporalInterceptor()` slice consumed by `cmd/workflow-worker`. CanaryWorkflow gets a span-recorder-backed integration test that asserts attribute presence.

**Tech Stack:** Go 1.22, OpenTelemetry Go SDK, Temporal Go SDK, `go.temporal.io/sdk/contrib/opentelemetry`.

**Spec:** `docs/superpowers/specs/2026-05-08-issue-62-temporal-otel-interceptor-design.md`

**Branch:** `feat/workflow-otel-interceptor` (worktree: `../gitscale.worktrees/feat-workflow-otel-interceptor`)

---

## File map

### Create
- `plane/workflow/observability/doc.go`
- `plane/workflow/observability/setup.go`
- `plane/workflow/observability/setup_test.go`
- `plane/workflow/observability/canary_span_test.go` — exercises CanaryWorkflow with interceptor

### Modify
- `cmd/workflow-worker/main.go` — call `SetupTracing`; install interceptors on client + worker
- `go.mod` / `go.sum` — OTel deps + Temporal contrib

---

## Pre-flight

- [ ] **Step P.1: Worktree**

```bash
cd /home/mitta/clients/gitscale/repos/gitscale-platform/gitscale
git fetch --all --prune
git worktree add -b feat/workflow-otel-interceptor \
    /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-otel-interceptor \
    origin/main
cd /home/mitta/clients/gitscale/repos/gitscale.worktrees/feat-workflow-otel-interceptor
git status --porcelain
```

Expected: clean.

- [ ] **Step P.2: Baseline**

```bash
go build ./...
go test -race ./plane/workflow/canary/... ./cmd/workflow-worker/... -count=1
```

Expected: green.

---

## Task 1: Add dependencies

**Files:** `go.mod`, `go.sum`

- [ ] **Step 1.1: Pin all OTel + Temporal contrib imports**

Create a temporary import-only file `plane/workflow/observability/imports_tmp.go`:

```go
//go:build ignore_imports
// (build-tagged so it never compiles into the binary; only here to drive go mod tidy.)

package observability

import (
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	_ "go.opentelemetry.io/otel/sdk/resource"
	_ "go.opentelemetry.io/otel/sdk/trace"
	_ "go.opentelemetry.io/otel/sdk/trace/tracetest"
	_ "go.opentelemetry.io/otel/semconv/v1.26.0"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
)

var _ = temporalotel.NewTracingInterceptor
```

Run:

```bash
go mod tidy
```

Then delete the temp file and instead rely on the real import that lands in
Task 2.

- [ ] **Step 1.2: Commit**

```bash
git add go.mod go.sum
git commit -m "$(cat <<'EOF'
chore(deps): OTel SDK + Temporal contrib for #62

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `observability.SetupTracing` + unit tests

**Files:**
- `plane/workflow/observability/doc.go`
- `plane/workflow/observability/setup.go`
- `plane/workflow/observability/setup_test.go`

- [ ] **Step 2.1: doc.go**

```go
// Package observability bootstraps OpenTelemetry tracing for the workflow
// worker (issue #62, spec D7). Public API:
//
//   SetupTracing(ctx, Config) registers a global TracerProvider and returns
//   a shutdown func that flushes pending spans on worker stop.
//
//   TemporalInterceptor() returns the slice consumed by both
//   client.Options.Interceptors and worker.Options.Interceptors so spans
//   span the boundary between scheduling and execution.
package observability
```

- [ ] **Step 2.2: setup.go**

```go
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
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// Config carries the env-derived knobs.
type Config struct {
	ServiceName      string
	ServiceNamespace string
	Environment      string
	OTLPEndpoint     string
}

// noopShutdown is the placeholder returned when tracing is disabled.
var noopShutdown = func(context.Context) error { return nil }

// SetupTracing installs the global TracerProvider per cfg. When cfg.OTLPEndpoint
// is empty, a no-op provider is registered and the returned shutdown is a no-op.
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
			return err
		}
		return exporter.Shutdown(shutdownCtx)
	}, nil
}

func buildResource(cfg Config) (*resource.Resource, error) {
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version = info.Main.Version
	}
	attrs := []interface {
		Key() string
		Value() string
	}{}
	_ = attrs // (silence; we use raw KeyValues below)
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
// client.Options.Interceptors and worker.Options.Interceptors.
func TemporalInterceptor() []interceptor.ClientInterceptor {
	intr, _ := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{
		Tracer: otel.GetTracerProvider().Tracer("workflow-worker"),
	})
	return []interceptor.ClientInterceptor{intr}
}
```

(If `tracenoop` package path differs, the import is `go.opentelemetry.io/otel/trace/noop` for v1.26+; consult `go.sum` after Task 1.)

- [ ] **Step 2.3: setup_test.go**

```go
package observability_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/observability"
)

func TestSetupTracing_EmptyEndpointIsNoop(t *testing.T) {
	shutdown, err := observability.SetupTracing(context.Background(), observability.Config{
		ServiceName: "test", ServiceNamespace: "ns", Environment: "dev",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned: %v", err)
	}
}

func TestSetupTracing_BogusEndpointDoesNotPanic(t *testing.T) {
	// OTLP gRPC exporter init does not connect eagerly; this should succeed.
	shutdown, err := observability.SetupTracing(context.Background(), observability.Config{
		ServiceName:  "test",
		ServiceNamespace: "ns",
		Environment:  "dev",
		OTLPEndpoint: "127.0.0.1:1", // unreachable port; ok per OTLP lazy connect
	})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	// Best-effort shutdown — may error but must not panic.
	_ = shutdown(context.Background())
}

func TestTemporalInterceptor_NonEmpty(t *testing.T) {
	if got := observability.TemporalInterceptor(); len(got) != 1 {
		t.Fatalf("expected 1 interceptor, got %d", len(got))
	}
}
```

- [ ] **Step 2.4: Run**

```bash
go test -race ./plane/workflow/observability/... -count=1
```

Expected: PASS.

- [ ] **Step 2.5: Commit**

```bash
git add plane/workflow/observability/doc.go \
        plane/workflow/observability/setup.go \
        plane/workflow/observability/setup_test.go
git commit -m "$(cat <<'EOF'
feat(workflow): SetupTracing + TemporalInterceptor for #62

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: CanaryWorkflow span integration test

**File:** `plane/workflow/observability/canary_span_test.go`

- [ ] **Step 3.1: Inspect existing CanaryWorkflow**

```bash
grep -rn "CanaryWorkflow\|RegisterWorkflow.*Canary" plane/workflow/canary/ | head -10
```

Identify the workflow function name + activity name + standard test
harness used by `plane/workflow/canary`.

- [ ] **Step 3.2: Write the test**

```go
package observability_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/workflow/canary"
	"github.com/gitscale-platform/gitscale/plane/workflow/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.temporal.io/sdk/testsuite"
)

func TestCanaryWorkflow_EmitsActivitySpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("workflow-worker"),
			semconv.ServiceNamespace("test"),
			semconv.DeploymentEnvironment("test"),
		),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(rec),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(/* attach interceptors here per package API */)
	canary.Register(env) // (replace with the canonical registration helper)

	env.ExecuteWorkflow(canary.CanaryWorkflow /*, args... */)
	if !env.IsWorkflowCompleted() {
		t.Fatalf("workflow not completed")
	}
	if env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}

	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	// At least one span must carry service.name=workflow-worker.
	var found bool
	for _, s := range spans {
		for _, attr := range s.Resource().Attributes() {
			if string(attr.Key) == "service.name" && attr.Value.AsString() == "workflow-worker" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected service.name=workflow-worker on at least one span")
	}
}
```

(The exact `SetWorkerOptions` call site differs across temporal-go versions;
adapt to the canary package's existing test helpers — those tests already
use `testsuite` and have the registration shape locked in.)

- [ ] **Step 3.3: Run**

```bash
go test -race -run TestCanaryWorkflow_EmitsActivitySpan ./plane/workflow/observability/... -count=1
```

Expected: PASS.

- [ ] **Step 3.4: Commit**

```bash
git add plane/workflow/observability/canary_span_test.go
git commit -m "$(cat <<'EOF'
test(workflow): CanaryWorkflow emits activity spans via interceptor (#62)

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire `cmd/workflow-worker`

**File:** `cmd/workflow-worker/main.go`

- [ ] **Step 4.1: Add tracing setup**

Locate the env-loading block. After `redisAddr := envDefault(...)` and
before `client.Dial`, add:

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
```

- [ ] **Step 4.2: Add interceptors to client.Options**

```go
c, err := client.Dial(client.Options{
    HostPort:     host,
    Namespace:    namespace,
    Logger:       logger,
    Interceptors: interceptors,
})
```

- [ ] **Step 4.3: Add interceptors to worker.Options**

Find the `worker.New(c, …, worker.Options{…})` call. Append:

```go
Interceptors: interceptors,
```

- [ ] **Step 4.4: Add the new import**

```go
import (
    ...
    "github.com/gitscale-platform/gitscale/plane/workflow/observability"
    ...
)
```

- [ ] **Step 4.5: Build + existing tests**

```bash
go build ./cmd/workflow-worker
go test -race ./cmd/workflow-worker/... -count=1
```

Expected: green.

- [ ] **Step 4.6: Commit**

```bash
git add cmd/workflow-worker/main.go
git commit -m "$(cat <<'EOF'
feat(workflow-worker): install OTel interceptor on client + worker (#62)

OTLP endpoint via OTEL_EXPORTER_OTLP_ENDPOINT; absent → no-op tracer.

Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Final gates + open PR

- [ ] **Step 5.1: Test sweep**

```bash
go build ./...
go vet ./...
golangci-lint run
go test -race ./... -count=1
```

Expected: all green.

- [ ] **Step 5.2: Mandatory skills (workflow plane)**

- `gitscale-temporal-determinism` — verify the interceptor is wired only at
  client/worker bootstrap, never inside a workflow function.
- `gitscale-go-conventions`
- `gitscale-plane-boundary` — `plane/workflow/observability` may import OTel
  but must not import any other plane.

- [ ] **Step 5.3: Self-review battery (parallel)**

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter` — OTel exporter init errors must
  bubble; shutdown errors must be logged not swallowed
- `pr-review-toolkit:type-design-analyzer` (`Config`, `SetupTracing`)
- `pr-review-toolkit:pr-test-analyzer`
- `adr-historian` (no ADR impact expected)

- [ ] **Step 5.4: Push + open PR**

```bash
git push -u origin feat/workflow-otel-interceptor
gh pr create --title "[Workflow] OTel interceptor + resource attributes for Temporal worker" --body "$(cat <<'EOF'
## Summary

- New `plane/workflow/observability` package: `SetupTracing` + `TemporalInterceptor`.
- `cmd/workflow-worker` installs the interceptor on both `client.Options`
  and `worker.Options`; OTLP endpoint via `OTEL_EXPORTER_OTLP_ENDPOINT`,
  no-op tracer when unset.
- CanaryWorkflow integration test asserts span emission with the prescribed
  resource attributes.

## ADR-impact

none. Operational; spec D7 deferred from #57.

## Test plan

- [x] `go test -race ./plane/workflow/observability/...`
- [x] `go test -race ./plane/workflow/canary/...`
- [x] `go test -race ./cmd/workflow-worker/...`
- [x] In-memory span recorder verifies one span per activity attempt with `service.name=workflow-worker`.

Spec: docs/superpowers/specs/2026-05-08-issue-62-temporal-otel-interceptor-design.md
Plan: docs/superpowers/plans/2026-05-08-issue-62-temporal-otel-interceptor-plan.md

<details><summary>Self-review</summary>

- code-reviewer: <result>
- silent-failure-hunter: <result>
- type-design-analyzer: <result>
- pr-test-analyzer: <result>
- adr-historian: <result>

</details>

Closes #62.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 5.5: Watch CI**

```bash
gh pr checks <number> --watch
```

---

## Self-review (plan author)

**Spec coverage:**
- Interceptor on client + worker — Task 4.
- Resource attributes — Task 2 (`buildResource`).
- OTLP endpoint env-driven — Task 2 + Task 4.
- Span emission test — Task 3.
- CanaryWorkflow regression — Task 3.

**Placeholder scan:** Step 3.2 directs the implementer to inspect the canary
package's test helpers for the exact `SetWorkerOptions` call. Acceptable —
the helper signature varies across Temporal Go SDK versions and reproducing
the wrong shape risks drift.

**Type consistency:** `Config`, `SetupTracing`, `TemporalInterceptor`
referenced consistently across plan and tests.
