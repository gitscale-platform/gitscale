---
name: gitscale-agent-quota-check
description: Use when adding or modifying any code path that handles agent identity, authentication, request admission, rate limiting, quota accounting, billing counters, or token metering. Triggers on changes in plane/edge/identity, plane/edge/metering, plane/application/auth, anything stamping a SPIFFE JWT-SVID, anything reading or writing DragonflyDB rate-limit keys, and on new HTTP/gRPC request handlers that aren't gated by an existing middleware. Also triggers on "do I need to meter this?", "is this rate-limited?", "is this on the agent traffic class?". Agent traffic dominates by design — an unmetered, unauthed path becomes a denial-of-quota vector or a free billing leak the moment it ships.
---

# GitScale Agent Quota Check

## Overview

Two of GitScale's three core principles converge on the request-admission layer:

1. **Agents are the primary traffic class.** Every endpoint is reached by agents at orders-of-magnitude higher rates than humans. Default-allow is wrong; default-meter is right.
2. **Metering is infrastructure, not a feature.** Rate limits, quota accounting, and billing counters are first-class concerns. They are stamped at the edge in the same hop as identity resolution.

ADR-012 binds service identity to SPIRE/SPIFFE — every workload presents and verifies a JWT-SVID per request. ADR-011 puts the rate-limit and identity caches on DragonflyDB.

This skill catches three failure modes:

1. **Unmetered path** — new endpoint shipped without rate limit / quota counter.
2. **Unidentified path** — new endpoint accepts agent traffic without verifying SPIFFE JWT-SVID.
3. **Wrong-tier metering** — rate limit applied per-IP or per-user when it should be per-agent-identity (or vice versa for human paths).

**Core principle:** if a request can reach business logic without crossing the identity-resolution + token-metering hop, it bypasses the design. Block it at the edge.

## When to Use

Trigger on **any** of:

- A new HTTP route, gRPC method, or WASM filter is added under `plane/edge/...`, `plane/application/api/...`
- A handler is added that isn't wrapped in the project's standard `auth + meter` middleware chain
- A SPIFFE / SPIRE / JWT-SVID code path is added, removed, or modified
- DragonflyDB rate-limit keys are read or written outside the metering layer
- A billing counter (`pkg/billing/counters/...` or equivalent) is incremented from a new path
- Token-bucket or sliding-window logic is hand-rolled in a handler instead of using the shared limiter
- The user asks "do I need to meter this?", "rate limit per what?", "is this an agent path?", "do I need SPIFFE here?"

**Don't trigger** for: internal-only RPCs already gated by SPIRE at the cluster mesh level, health-check endpoints, metrics scrape endpoints (those are metered separately at the scrape side).

## The admission rules

Every request that reaches business logic must have, in order:

1. **Identity verified** — SPIFFE JWT-SVID present, signature valid, not expired, audience matches the called service.
2. **Identity classified** — agent vs. human vs. internal. Each tier has different quota tables.
3. **Quota deducted** — token bucket or counter decremented in DragonflyDB. If the bucket is empty, return `429` (or gRPC `RESOURCE_EXHAUSTED`).
4. **Counter incremented** — billing counter for the right account is bumped on the way out, not at request start (failed requests don't bill).

| Path origin | Identity check | Quota dimension | Billing |
|---|---|---|---|
| External agent | SPIFFE JWT-SVID, agent class | per agent identity | yes |
| External human | OAuth bearer or session, human class | per user account | yes |
| Internal service | SPIFFE JWT-SVID, service class | per source service | no |
| Webhook out | n/a (we initiate) | per destination URL (back-pressure) | n/a |

## Workflow

1. **Locate the new entry point** in the diff (route, gRPC method, WASM filter).
2. **Trace the middleware chain** from entry to handler. Verify the chain includes (in some order): SPIFFE verify → classify → quota → handler → billing increment.
3. **If quota is keyed**, verify the key matches the path origin row in the table above. Per-IP on an agent path is wrong.
4. **If billing is incremented**, verify it's after handler success, not before.
5. **Output a verdict.**

## Output Format

```
agent-quota: <ok | violation>
Entry points checked: <route/method | none>
Missing in chain: <auth | classify | quota | billing | none>
Wrong-tier metering: <found at file:line | none>
Fix: <concrete middleware addition or correction>
```

## Example

**Input diff:**

```go
// plane/application/api/router.go
r.HandleFunc("/v1/repos/{id}/contents", s.GetContents).Methods("GET")
// no middleware wrapper ❌

// plane/application/repos/contents.go
func (s *Service) GetContents(w http.ResponseWriter, r *http.Request) {
    repoID := mux.Vars(r)["id"]
    blob, _ := s.git.Read(r.Context(), repoID)
    w.Write(blob)
}
```

**Verdict:**

```
agent-quota: violation
Entry points checked: GET /v1/repos/{id}/contents
Missing in chain: auth, classify, quota, billing
Wrong-tier metering: n/a (no metering at all)
Fix:
  Wrap the handler in the standard chain:
    r.Handle("/v1/repos/{id}/contents",
      middleware.SPIFFEVerify(
        middleware.Classify(
          middleware.Quota(
            middleware.BillOnSuccess(
              http.HandlerFunc(s.GetContents),
            ),
          ),
        ),
      ),
    ).Methods("GET")
  Or, preferably, register via the project's `mux.AgentRoute(...)` helper which
  applies the chain in one call.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| Rate limit set per-IP for an agent endpoint | Agents share IPs (NAT, cloud egress). Limit per SPIFFE identity. |
| Billing counter incremented before handler runs | Failed requests must not bill. Move increment after handler success. |
| New endpoint added with `// TODO: add auth later` | "Later" is when the endpoint is already in production traffic. Add the chain in the same PR. |
| SPIFFE check disabled in dev / test config and forgotten in prod | Use the same chain in dev with a permissive cert. Don't disable the verifier; weaken the policy. |
| Hand-rolling a token bucket in the handler | The shared limiter in `pkg/limiter/` is correctly atomic across replicas via DragonflyDB. Hand-rolled in-process buckets fail under multi-replica deploys. |
| Treating a webhook receiver as "no auth needed because we own it" | Webhook receivers also need SPIFFE — they are services in the mesh. The mesh admission rule applies. |

## Why This Matters

A single unmetered endpoint at agent traffic rates is a quota-burn vector that can drain the platform's budget within hours. A single unauthed endpoint that touches user content is a privacy incident waiting to happen. The cost of forgetting the chain on one route is unbounded; the cost of including it correctly is two extra middleware lines.

Metering at the edge is also load-shedding insurance: when a runaway agent loops, the limiter takes the hit, not the database. Without the limiter, every plane downstream feels the pressure.
