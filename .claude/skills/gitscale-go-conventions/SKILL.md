---
name: gitscale-go-conventions
description: Use when writing or editing any .go file in the GitScale repo. Triggers on new package creation, new function declarations, new error sites, new context-handling code, panic/recover usage, dependency injection wiring, struct construction, init() function appearance, and on user questions like "is this idiomatic Go for this repo?", "should this return an error?", "how do I structure this package?", "is panic ok here?". Catches drift from project Go conventions before it crystallizes — Go style debt compounds fast and is painful to fix later when 50 packages have wandered.
---

# GitScale Go Conventions

## Overview

Go is the primary language across the application, git, edge (filter glue), workflow, and data planes. GitScale is a long-lived service codebase with many engineers, agents, and automated tools touching it. Consistent Go conventions matter more here than in a small library.

This skill captures the rules that go *beyond* what `gofmt`, `goimports`, and `golangci-lint` already enforce — the judgment calls that an opinionated codebase makes.

**Core principle:** errors are values, contexts thread through every blocking call, panics never escape package main, and packages are organized by responsibility rather than by type.

## When to Use

Trigger on **any** of:

- A new `.go` file is created
- A new package is created (new directory under `plane/...` or `pkg/...`)
- A new function returns or accepts an `error`, `context.Context`, or both
- A `panic`, `recover`, or `init()` is added
- A struct field's tags are added or modified (`json`, `db`, `validate`)
- An interface is declared
- Dependency injection wiring is added (constructor function `NewX(...)`)
- The user asks "is this idiomatic?", "should this be an interface?", "where do errors go?", "is `init()` ok?"

**Don't trigger** for: generated code (clearly marked `// Code generated ... DO NOT EDIT.`), vendor directory.

## The rules

### Errors

- Always return errors as the **last** return value. Never `(error, T)`.
- Wrap errors at boundary crossings with `fmt.Errorf("%s: %w", op, err)` where `op` names the operation. Use `%w` for the cause; consumers use `errors.Is` / `errors.As`.
- Sentinel errors (`var ErrNotFound = errors.New(...)`) for known states callers branch on. Custom error types only when a caller needs structured fields.
- **Don't** swallow errors with `_ = call()`. If the error is genuinely safe to ignore, write a one-line comment explaining why. If it isn't, surface it.
- **Don't** log + return. Either log and handle, or return and let the caller decide. Doing both produces duplicated log lines from every layer.

### Context

- Every function that does I/O, blocks, or might be cancelled takes `ctx context.Context` as its **first** parameter.
- Never store a `context.Context` in a struct. Pass it through call sites.
- Never use `context.TODO()` outside of tests and one-shot scripts. Use `context.Background()` only at the entry point of a goroutine or program; everywhere else, propagate the parent.
- Don't create your own `context.Context` types. Use `context.WithValue` with package-local key types for request-scoped values, sparingly.

### Panics and `init()`

- Panic only in `package main` or in initialization code that fails fatally (program cannot start). Never in library code or request handlers.
- `recover()` is only acceptable at goroutine boundaries to prevent crashes; the recovered error must be logged with stack trace and surfaced as an error to the caller (or the supervisor).
- Avoid `init()` functions. They run at import time, ordering is fragile, and they hide dependencies. Use explicit `Init(...)` or constructor functions called from `main`.

### Package layout

- A package's directory name matches its package name. No `package utils` in `pkg/strings/`.
- One responsibility per package. If you can name two responsibilities, it's two packages.
- Public API at the top of files; unexported helpers below. Tests in `_test.go` siblings.
- The five plane directories (`plane/edge`, `plane/git`, `plane/application`, `plane/workflow`, `plane/data`) are the top-level seams. Within each, organize by domain (`pr`, `repo`, `auth`) rather than by layer (`handlers`, `services`, `models`).

### Interfaces

- Define interfaces **at the consumer site**, not at the producer site. The consumer knows what shape it needs.
- Keep interfaces small (often 1–3 methods). `io.Reader`, `io.Writer` are the model.
- Don't define an interface for "future test mocking." Define it when there are two implementations.

### Constructors and DI

- Use `func NewX(deps...) (*X, error)` or `func NewX(deps...) *X` if construction can't fail.
- Take dependencies as arguments. Don't reach into globals.
- Avoid functional options for simple cases (≤ 4 args). Use them when the option set is genuinely large or extensible.

### Concurrency

- A `goroutine` started inside a function must terminate before the function returns, OR be explicitly handed off to a supervisor (`errgroup.Group`, a long-lived worker pool).
- Channels are owned by the goroutine that closes them. Never close from the receiver side.
- Always pair `go func()` with `ctx` cancellation handling. A goroutine that doesn't observe context is a leak.

### Testing

- Use the standard library `testing`. Use `testify/assert` only when the assertion is genuinely complex.
- Table-driven tests for input-variation testing. One subtest per row via `t.Run(tc.name, func(t *testing.T) { ... })`.
- Mocks for external services (DB, Kafka, network) only. Don't mock the unit under test or its direct collaborators within the package.
- Test names: `TestThingName_Condition_ExpectedResult`.

## Quick Reference

| Topic | Rule |
|---|---|
| Error position | Last return value |
| Error wrapping | `fmt.Errorf("op: %w", err)` |
| Context position | First parameter |
| Context in struct | Never |
| Panic in handler | Never |
| `init()` | Avoid |
| Interface location | Consumer side |
| Goroutine | Bounded lifetime, ctx-aware |
| Test name | `TestX_Cond_Result` |

## Output Format (when used to review)

```
go-conventions: <ok | violations>
Flags:
  <file:line — rule violated — what to do>
golangci-lint: <run separately>
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| `func Foo() (Result, error)` for *some* funcs and `func Foo() (error, Result)` for others | Always `(T, error)`. Mixed positioning is a footgun for `if err != nil`. |
| `if err != nil { log.Error(err); return err }` everywhere | Pick one. The caller logs at the appropriate level once. |
| `ctx := context.Background()` in the middle of a request handler | Use the request's `ctx`. `context.Background()` in handlers loses cancellation/deadline propagation. |
| `panic("must not happen")` in a request path | Replace with `return fmt.Errorf("invariant: ...")`. The panic blows up the goroutine; the error blows up the request only. |
| Big interface defined in the producing package "for tests" | Define the interface where it's consumed. Producers expose concrete types. |
| `go doWork()` with no way to wait or cancel | Use `errgroup.Group` or a worker pool. Track lifetime. |
| Globals injected via package-level `var Service = ...` | Inject via constructors. Globals are untestable and hide dependencies. |

## Why This Matters

Go's standard library and `gofmt` make a lot of style decisions for you. The remaining decisions — error wrapping, context discipline, panic policy, package layout, interface placement — are where teams diverge. When divergence accumulates, every code review becomes a style negotiation, every onboarding takes weeks longer, and refactoring across packages becomes a search-and-replace job rather than a mechanical change.

The rules above are not novel — they are the consensus of Go's idiomatic literature applied to this codebase. Following them costs nothing at edit time. Not following them costs ongoing review friction and refactor debt.
