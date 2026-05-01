---
name: gitscale-plane-boundary
description: Use when adding or modifying Go imports, package layout, function signatures, or shared state inside plane/edge, plane/git, plane/application, plane/workflow, or plane/data. Triggers when one plane's package imports another plane's internal package, when a struct or channel is shared across plane boundaries, when a function returns a type defined in another plane's internal package, or when the user asks "is this a plane violation?", "can plane A call plane B?", "where should this code live?". Catches the second core principle (loose coupling at every seam) before it rots — cross-plane in-process coupling is invisible at PR time but cascades failures across planes once it's live.
---

# GitScale Plane Boundary

## Overview

GitScale's second core principle: **loose coupling at every seam**. Each plane (edge, git, application, workflow, data) must be independently deployable. A failure in one plane must not cascade to others.

This is enforced by code: no cross-plane imports of internal packages, no shared in-process state, no synchronous cross-plane function calls. Cross-plane communication goes through the service API only.

**Core principle:** if `plane/A/internal/...` imports `plane/B/internal/...`, the planes are no longer independent — they share a build, a process, and a failure surface. Block it at the seam.

## When to Use

Trigger on **any** of:

- A Go import path crosses a plane boundary (`plane/X/...` importing `plane/Y/...` where X ≠ Y)
- A new file is created under `plane/<X>/internal/` that exposes types meant for cross-plane use
- A package under `plane/<X>/` is imported by code outside its own plane (other than via the service API in `plane/<X>/api/`)
- A `var`, `chan`, `sync.Map`, or singleton is added that's read or written by more than one plane in the same process
- The user asks: "where does this code belong?", "can plane X call plane Y directly?", "is this a plane violation?"

**Don't trigger** for: `pkg/` shared utilities, `internal/proto/` generated code shared between planes via gRPC, test fixtures that span planes deliberately and are isolated to `_test.go` files.

## The boundary rule

| From | May import | May NOT import |
|---|---|---|
| `plane/<X>/...` | `plane/<X>/...`, `pkg/...`, `internal/proto/...`, std lib, third-party | `plane/<Y>/internal/...` (any Y ≠ X) |
| `plane/<X>/api/...` (the service API) | same as above | same as above |
| Cross-plane caller | `plane/<Y>/api/` over the wire (gRPC/HTTP) | `plane/<Y>/internal/` directly |
| `pkg/` | `pkg/`, `internal/proto/`, std/third-party | any `plane/<X>/` |

`plane/<X>/api/` is the only published surface of plane X. Treat it as a stable contract. Everything under `plane/<X>/internal/` is private.

## Workflow

1. **Scan the diff** for `import` statements added or modified.
2. For each, classify:
   - Same plane → fine.
   - `pkg/` or `internal/proto/` or std lib → fine.
   - Cross-plane targeting `api/` → fine, but verify it's a wire call (gRPC client / HTTP), not a direct function reference inside the same process.
   - Cross-plane targeting `internal/` → **block**.
3. **Scan for shared mutable state**: package-level `var`, `chan`, `sync.*` types, file-scoped singletons used by handlers from multiple planes.
4. **Output a verdict** — see format below.
5. If a violation is found, propose the fix: usually "expose this on plane/<Y>/api/ and call it via the gRPC client in plane/<X>/clients/<y>/".

## Output Format

```
plane-boundary: <ok | violation>
Imports flagged: <path → path | none>
Shared state flagged: <symbol at file:line | none>
Reasoning: <one sentence per flag>
Fix: <concrete suggestion, or "none needed">
```

## Example

**Input diff:**

```go
// plane/application/pr_engine/merge.go
import (
    "github.com/gitscale/gitscale/plane/git/internal/packwriter"  // ❌
    "github.com/gitscale/gitscale/plane/data/internal/outbox"     // ❌
)

func MergePR(ctx context.Context, prID string) error {
    pack := packwriter.New()
    return outbox.Write(ctx, "pr.merged", prID)
}
```

**Verdict:**

```
plane-boundary: violation
Imports flagged:
  plane/application/pr_engine → plane/git/internal/packwriter
  plane/application/pr_engine → plane/data/internal/outbox
Shared state flagged: none
Reasoning: pr_engine reaches into git plane's pack-writing internals and
  data plane's outbox internals; both are private to their planes.
Fix: call plane/git/api (PackService.WritePack RPC) and plane/data/api
  (OutboxService.Append RPC) via generated clients in
  plane/application/clients/git/ and plane/application/clients/data/.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| Putting "shared" types in one plane's `internal/` and importing from another | Move to `pkg/` or to a proto under `internal/proto/` |
| Importing `plane/<Y>/api/` directly into `plane/<X>/` Go code instead of using the gRPC client | API packages should expose only the gRPC service definition. Calls go through the wire. |
| Treating `pkg/` as a backdoor for plane-specific logic | `pkg/` is for utilities with no plane affinity (logging, IDs, time). If it's plane logic, it doesn't belong in `pkg/`. |
| Adding a global `var Cache = ...` shared between planes | Each plane keeps its own cache. Use the data plane's API if cross-plane visibility is needed. |
| "Just this once" — direct call for a hot path because RPC is slow | Hot paths get their own service with their own SLO. The cost of cross-plane RPC is the cost of the principle. Don't shortcut. |

## Why This Matters

If plane A panics, plane B should keep serving. If plane A is rolled back, plane B's deploy is unaffected. If plane A's schema changes, plane B's types don't shift under it. Every cross-plane in-process import is a hidden dependency that breaks one of these properties. The dependency is invisible until incidents stack up across planes — and by then, untangling the imports is a quarter of refactoring.

The goal is not bureaucracy. It's that an on-call engineer can look at one plane's binary and know the failure surface ends at its API.
