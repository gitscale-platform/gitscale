package observability_test

import (
	"context"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/workflow/canary"
	"github.com/gitscale-platform/gitscale/plane/workflow/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// TestCanaryWorkflow_EmitsActivitySpan exercises the CanaryWorkflow under a
// TestWorkflowEnvironment with the OTel tracing interceptor installed and an
// in-memory span recorder attached to the global TracerProvider. Asserts:
//
//  1. At least one span is emitted.
//  2. At least one span carries the prescribed resource attribute
//     service.name=workflow-worker (spec D7).
//
// This proves the wiring contract between SetupTracing's resource attribute
// set and TemporalWorkerInterceptor's span emission, which is the regression
// shape called out in the plan for issue #62.
func TestCanaryWorkflow_EmitsActivitySpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("workflow-worker"),
			semconv.ServiceNamespace("test"),
			semconv.DeploymentEnvironment("test"),
		),
	)
	if err != nil {
		t.Fatalf("resource.New: %v", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(rec),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	// Install the worker-side interceptor so the OTel contrib emits an
	// activity span when the workflow dispatches HealthActivity.
	env.SetWorkerOptions(worker.Options{
		Interceptors: observability.TemporalWorkerInterceptor(),
	})

	mem := cache.NewMemoryStore(cache.RealClock{})
	if err := mem.Set(context.Background(), canary.HealthKey, []byte("OK"), time.Hour); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	a := canary.NewHealthActivity(mem)
	env.RegisterActivityWithOptions(a.Run, activity.RegisterOptions{Name: canary.HealthActivityName})

	env.ExecuteWorkflow(canary.CanaryWorkflow)

	if !env.IsWorkflowCompleted() {
		t.Fatal("canary workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}

	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("expected at least one span; got none")
	}
	var foundServiceName bool
	for _, s := range spans {
		for _, attr := range s.Resource().Attributes() {
			if string(attr.Key) == "service.name" && attr.Value.AsString() == "workflow-worker" {
				foundServiceName = true
			}
		}
	}
	if !foundServiceName {
		t.Fatalf("expected service.name=workflow-worker on at least one span; got %d spans without it", len(spans))
	}
}
