# GraphQL API SLO — Phase 2 GA gate

Issue: [#113](https://github.com/gitscale-platform/gitscale/issues/113)
Service: `cmd/graphql-api`
Surface: `/graphql`, `/graphql/persisted/{hash}`, `/graphql/persisted/register`
Decision authority: application plane on-call + ADR Steward.

## Phase model

- **Phase 1 (preview):** ships behind `GRAPHQL_PREVIEW=true`. The flag is required at startup; missing it exits the process with a non-zero status. Phase 1 is unmetered against these SLOs.
- **Phase 2 (GA):** flips the surface to default-on by removing the `GRAPHQL_PREVIEW=true` precondition. The flip is a single env-var change; it requires all five gates below to be green for two consecutive measurement windows (7-day pre-prod, 14-day prod-pilot).

## Gates

| # | SLO | Target | Measurement |
|---|---|---|---|
| 1 | **Availability** | 99.9% successful responses | `sum(rate(graphql_requests_total{result="success"}[5m])) / sum(rate(graphql_requests_total[5m]))` ≥ 0.999. A response counts as `success` when HTTP=200 AND (`data` is non-null OR `errors[].extensions.code` ∈ {`VALIDATION_FAILED`, `FORBIDDEN`, `NOT_FOUND`, `COST_BUDGET_EXCEEDED`, `DEPTH_EXCEEDED`, `RATE_LIMITED`, `PERSISTED_QUERY_NOT_FOUND`}). `INTERNAL` is the only result that fails availability. |
| 2 | **Latency** | P95 ≤ 250ms persisted; P95 ≤ 600ms ad-hoc | `histogram_quantile(0.95, sum by (le, persisted) (rate(graphql_request_duration_seconds_bucket[5m])))` for each `persisted` label value. |
| 3 | **Cost-rejection accuracy** | False-positive rate < 0.1% | Shadow-execute on a 1% sample of rejected queries; counter `graphql_cost_false_positive_total / graphql_cost_rejected_total` < 0.001 over the window. |
| 4 | **Persisted-query hit rate** | ≥ 60% of agent traffic | `sum(rate(graphql_requests_total{persisted="true",principal_kind="agent"}[1h])) / sum(rate(graphql_requests_total{principal_kind="agent"}[1h]))` ≥ 0.6. |
| 5 | **Schema-compat drift** | 0 unexpected field-name diffs vs GitHub snapshot | `make lint-graphql` runs in CI; the named-subset compat test fails the build on any drift. Alert on first occurrence. |

## Telemetry surface

The runtime publishes the following Prometheus series (registration lands with #113):

- `graphql_requests_total{result, persisted, principal_kind, op_kind}` — request counter.
- `graphql_request_duration_seconds_bucket{persisted, op_kind}` — histogram.
- `graphql_cost_total{principal_kind}` — Σ Complexity.
- `graphql_cost_rejected_total{code}` — pre-execution rejections.
- `graphql_cost_false_positive_total` — incremented by the shadow-execute path.

## GA decision template

A go/no-go decision is made by inspecting the dashboard `graphql/ga-readiness` over the most recent two windows. The decision form lives in `docs/runbooks/graphql-ga-readiness.md` (deferred — opened with the GA-flip follow-up issue, not this PR).

## Out-of-scope for Phase 2

- Subscription transport (deferred).
- `Upload` scalar (deferred).
- Apollo Federation (explicit non-goal per spec).
