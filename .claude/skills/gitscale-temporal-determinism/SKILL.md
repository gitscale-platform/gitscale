---
name: gitscale-temporal-determinism
description: Use when writing or modifying Temporal workflow code under plane/workflow, when adding a new workflow function, when calling time, random, system, or any non-deterministic API from inside a workflow, when iterating maps in a workflow, when launching goroutines or channels in workflow code, when modifying activity timeouts or retry policies, or when the user asks "is this deterministic?", "can I call X from a workflow?", "why does my workflow replay fail?". Non-determinism in Temporal workflows is undetectable in unit tests but lethal at replay — a workflow that replays differently than it executed will get stuck mid-execution and require manual repair.
---

# GitScale Temporal Determinism

## Overview

Temporal workflows are recorded as a history of events. On replay (worker restart, version upgrade, query), Temporal re-runs the workflow function and matches each step against the recorded history. **Any divergence between execution and replay corrupts the workflow.** That divergence is called non-determinism.

The rule has two halves:

1. **Workflow functions must be deterministic.** Same inputs → same sequence of decisions, every time.
2. **Anything non-deterministic must be moved to an Activity.** Activities run once, their result is recorded in history, and replay reads the recorded result.

This skill catches the common ways non-determinism leaks into workflow code.

**Core principle:** if it can return different values across two calls, it cannot be in the workflow function. It must be in an activity.

## When to Use

Trigger on **any** of:

- A new file is added under `plane/workflow/workflows/...` (or wherever workflow funcs live)
- A function with the signature `func ... (ctx workflow.Context, ...) (..., error)` is created or modified
- A workflow function calls `time.Now()`, `time.Since()`, `rand.*`, `os.*`, `net.*`, `http.*`, or any I/O directly
- A workflow function uses `range` over a `map`
- A workflow function starts goroutines (`go func()`) or uses raw channels (`make(chan ...)`) — Temporal has its own primitives
- An activity's timeout, retry policy, or heartbeat is modified
- A workflow uses `workflow.GetVersion(...)` for versioning logic
- The user asks "is this deterministic?", "can I call X here?", "why is my replay failing?"

**Don't trigger** for: activity functions (those run once, can do anything), workflow tests using `testsuite.WorkflowTestSuite`, code outside `plane/workflow`.

## The forbidden APIs (in workflow code)

| API | Why forbidden | Replacement |
|---|---|---|
| `time.Now()`, `time.Since()`, `time.After()` | Wall clock differs between execution and replay | `workflow.Now(ctx)`, `workflow.NewTimer(ctx, d)` |
| `rand.*`, `crypto/rand.*` | Different value per call | Activity that generates and returns the random value, or `workflow.SideEffect` |
| `uuid.New()` and friends | Same problem as random | Same fix |
| `os.Getenv`, `os.Hostname`, `os.PID` | Environment may differ | Pass as workflow input, or read in activity |
| `net.*`, `http.*`, raw socket I/O | Network calls aren't reproducible | Move to activity |
| `go func()` (raw goroutines) | Scheduling order isn't deterministic | `workflow.Go(ctx, func(ctx workflow.Context) { ... })` |
| `make(chan ...)`, `<-ch`, `ch <-` | Same scheduling problem | `workflow.NewChannel(ctx)`, signal channels |
| `range` over a `map` | Map iteration order is non-deterministic | Sort keys first, then iterate |
| `select` over Go channels | Mixes scheduler with workflow | `workflow.Selector` |
| Direct DB / Kafka / file I/O | Side effects that aren't recorded | Move to activity |
| `panic` recovery in workflow | Recovered panics may not replay identically | Let Temporal's worker handle it; failure is the SDK's job |

## The escape hatches

When you genuinely need non-deterministic behavior in a workflow, use one of these — and only these — primitives:

- **`workflow.SideEffect(ctx, func() any)`**: runs the function once, records the result. Cheap. Use for small computations like `uuid.New()`.
- **Activity**: heavier, retryable, cancellable, has its own timeout. Use for anything I/O-bound.
- **Signals**: external events delivered to the workflow. The signal payload is recorded; reading it is deterministic.
- **`workflow.GetVersion(ctx, changeID, minSupported, maxSupported)`**: for code-versioning when changing workflow logic across releases. Without it, in-flight workflows can replay against the old logic and break.

## Workflow

1. **Confirm the function is a workflow** — signature is `func X(ctx workflow.Context, ...) ...`, registered via `worker.RegisterWorkflow`.
2. **Scan for forbidden APIs** in the function body and any function it calls (transitively, until you reach an activity boundary).
3. **For each violation**, propose either an activity (if I/O-bound) or `workflow.SideEffect` (if pure but non-deterministic).
4. **Check map iteration**: any `range` over a `map[T]U` should be over a sorted key slice.
5. **Check goroutines and channels**: replace raw with `workflow.Go` and `workflow.NewChannel`.
6. **For activities**: verify timeout is set (no defaults — defaults are unreasonably long), retry policy is appropriate.
7. **Output a verdict** with file:line citations.

## Output Format

```
temporal-determinism: <ok | violations>
Workflow funcs touched: <name | name, name>
Forbidden API calls:
  <file:line — call — replacement>
Map iteration:
  <file:line — key type | none>
Raw goroutines/channels:
  <file:line — replacement | none>
Activity config:
  <activity name — timeout=? retry=? — concerns | ok>
Fix: <bulleted concrete changes>
```

## Example

**Input diff:**

```go
// plane/workflow/workflows/repo_indexing.go
func IndexRepoWorkflow(ctx workflow.Context, repoID string) error {
    started := time.Now()                          // ❌
    jobID := uuid.New().String()                   // ❌
    for k, v := range repoTable {                  // ❌ map range
        // ...
    }
    go func() {                                    // ❌ raw goroutine
        emit(jobID, started)
    }()
    err := workflow.ExecuteActivity(ctx, FetchObjects, repoID).Get(ctx, nil)
    return err
}
```

**Verdict:**

```
temporal-determinism: violations
Workflow funcs touched: IndexRepoWorkflow
Forbidden API calls:
  plane/workflow/workflows/repo_indexing.go:2 — time.Now() — workflow.Now(ctx)
  plane/workflow/workflows/repo_indexing.go:3 — uuid.New() — workflow.SideEffect(ctx, func() any { return uuid.New().String() })
Map iteration:
  plane/workflow/workflows/repo_indexing.go:4 — keys must be sorted before range
Raw goroutines/channels:
  plane/workflow/workflows/repo_indexing.go:7 — replace with workflow.Go(ctx, func(ctx workflow.Context) { ... })
Activity config:
  FetchObjects — timeout/retry not set in this call site, verify ActivityOptions on ctx
Fix:
  - started := workflow.Now(ctx)
  - var jobID string
    workflow.SideEffect(ctx, func(ctx workflow.Context) any { return uuid.New().String() }).Get(&jobID)
  - keys := slices.Sorted(maps.Keys(repoTable))
    for _, k := range keys { v := repoTable[k]; ... }
  - workflow.Go(ctx, func(ctx workflow.Context) { emit(jobID, started) })
  - Add: ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5*time.Minute, RetryPolicy: ...}) before ExecuteActivity
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| "It worked in tests, must be fine" | Test suite often replays predictably; production replay (after worker upgrade, after stuck-workflow recovery) is where non-determinism shows. Trust the rules over the test outcome. |
| Using `time.Sleep` in workflow code | `workflow.Sleep(ctx, d)`. `time.Sleep` blocks the worker thread and isn't recorded. |
| Putting all logic into one giant workflow | Workflows are coordinators. Heavy logic goes into activities. A 1000-line workflow function is a smell. |
| `workflow.SideEffect` for I/O | `SideEffect` runs in the worker and isn't retryable, isn't cancellable, has no timeout. Anything I/O must be an activity. |
| Versioning workflow logic without `workflow.GetVersion` | In-flight workflows started under old logic will replay against new logic and crash. Always gate behavior changes on `GetVersion`. |
| Storing workflow IDs in maps and iterating to dispatch signals | Use sorted iteration. Or, better, model the dispatch as child workflows / signal-with-start. |
| Setting an activity timeout to "long enough" without thinking | Activity timeout = how long until Temporal gives up and retries. Set it as low as you can — fast failure is better than slow drift. |

## Why This Matters

Temporal's superpower — durable, replay-safe workflows that survive worker crashes and hardware changes — depends entirely on workflow code being deterministic. Get it wrong and the failure mode is the worst kind: workflows hang in production, stuck on a step they can't complete because the replay diverges from history. Operators have to manually repair them, often by terminating and restarting from a checkpoint, losing in-flight state.

The cost of being deterministic is low — a handful of Temporal-flavoured replacements for stdlib calls. The cost of *not* being deterministic is hours of incident response per stuck workflow, multiplied by however many were started before the bug shipped.
