# Plan — Issue #115: Plan-approval full policy DSL + escalation (ADR-019)

- Spec: `docs/superpowers/specs/2026-05-09-issue-115-plan-approval-policy-dsl-design.md`
- Issue: #115
- Branch: `feat/application-plan-approval-policy-dsl`
- Subagent: `application-plane`
- Mandatory pre-commit skills: `gitscale-adr-guard`, `gitscale-go-conventions`, `gitscale-temporal-determinism`, `gitscale-plane-boundary`
- ADR review at PR-time: `comprehensive-review:architect-review` (touches plane boundary, adds Kafka topics, modifies ADR-referenced code paths)

## Tasks (commit boundaries)

### Task 1 — DSL types + validation (commit 1)

- Create `plane/application/policy/dsl.go` with `Policy`, `Rule`, `PredicateKind` (closed enum: 5 kinds), `ApproverGroup`, `EscalationRung`.
- Create `plane/application/policy/validate.go` returning closed-enum error codes (`policy_invalid_predicate_kind`, `policy_empty_ladder`, `policy_unknown_approver_group`, `policy_zero_required_count`, `policy_non_human_approver`).
- Unit tests covering each error code + happy path.
- No DB or HTTP yet.

**DoD**: `go vet ./plane/application/policy/...` clean; `go test ./plane/application/policy/...` passes; `gitscale-go-conventions` clean.

### Task 2 — PolicyStore interface + PG impl + compliance (commit 2)

- `plane/application/policy/store.go` — `PolicyStore` interface: `CreatePolicy`, `GetPolicy`, `ListPolicies`, `UpdatePolicy`, `DeletePolicy`, `CreatePlan`, `GetPlan`, `UpdatePlanStatus`, `AppendAudit`.
- `plane/application/policy/pgstore.go` — PG impl using injected `MetadataStore.Transact`. Every mutating op writes outbox row in same Tx.
- `plane/data/schema/migrations/NNNN_application_policies.sql` — three tables per spec.
- `plane/application/policy/compliance/` — compliance suite per ADR-017 pattern.
- HMAC signing of `body_json` using org KEK from existing keystore (ADR-011 pattern, lookup by `org_id`).

**DoD**: `make lint-events` (outbox check) clean; compliance suite green against PG testcontainer; `gitscale-outbox-check` clean.

### Task 3 — Merkle audit chain (commit 3)

- `plane/application/policy/audit.go` — `AppendAudit(ctx, policyID, planID, kind, actor, payload)` computes `prev_hash` via `SELECT row_hash FROM application.policy_audit WHERE policy_id=$1 ORDER BY id DESC LIMIT 1 FOR UPDATE`; returns 32 zero bytes if empty.
- Canonical-JSON helper for stable hashing (sorted keys, no whitespace).
- Tamper-detection unit test: replay chain, mutate one row's `payload_json`, verify `row_hash` no longer matches recomputed hash.

**DoD**: chain replay passes from genesis to N for N=1, 100, 1000; tamper test fails verification.

### Task 4 — Engine: predicate matching + plan submission (commit 4)

- `plane/application/policy/engine.go` — `Engine.SubmitPlan(ctx, in) (Decision, error)`.
- `plane/application/policy/predicates.go` — one matcher per `PredicateKind`. `pr_merge` matches on `match.branch`; `force_push` on `match.ref_pattern`; `production_deploy` on `match.environment`; `bulk_action` on `len(actions) >= threshold`; `agent_default` on agent-proposed AND no other rule matched.
- First-match-wins semantics; no-match emits `policy_audit` row `auto_approved_no_rule` and returns auto-decision.
- Unit tests: one per predicate kind matching + non-matching; ordering test (rule precedence); no-match audit-row test.

**DoD**: `gitscale-adr-guard` clean (engine logic stays in application plane).

### Task 5 — gRPC service surface (commit 5)

- `plane/application/policy/grpc.go` — implements `PolicyServiceServer`: `SubmitPlan`, `GetPlanDecision` (server-streaming for long-poll), `RecordDecision`, `EscalateRung`.
- Proto: `proto/policy/v1/policy.proto` — service + messages. Generates into `plane/application/policy/policypb/`.
- `plane/workflow/appclient/policy.go` — typed client wrapper consumed by workflow plane.
- gRPC server registers in existing application-plane gRPC bootstrap (precedent: billing service from #74).

**DoD**: `gitscale-plane-boundary` clean (no `plane/data/store` import from `plane/workflow/approval/`).

### Task 6 — ApprovalActivity (workflow plane) (commit 6)

- `plane/workflow/approval/activity.go` — `ApprovalActivity` with `appclient.PolicyClient` dependency only.
- `Execute` flow: `SubmitPlan` → `GetPlanDecision` long-poll with deadline = `rung.SLASeconds` + 5s jitter → on deadline-exceeded, call `EscalateRung` with `current_rung+1`; on final-rung `auto_deny` configured, return `Decision{Status: rejected}`.
- Bundle in `plane/workflow/approval/bundle.go` per existing registry pattern.
- Determinism: no time/random/network calls outside activities; workflow code (if any wrappers) uses `workflow.Now`/`workflow.NewTimer`.

**DoD**: `gitscale-temporal-determinism` clean; `make lint-determinism` clean.

### Task 7 — REST endpoints atop #111 router (commit 7)

- Wire policy CRUD + plan endpoints into the `plane/application/restapi` router (handles from #111).
- Auth middleware: org admin gate for CRUD; agent/service gate for plan submission; HumanUser-in-approver-group gate for decisions.
- Feature flag `policy_engine.full_dsl` check; off → 503 `feature_not_enabled`.
- Error envelope per #111 closed-enum codes; new codes: `plan_already_decided`, `plan_expired`, `decision_unauthorized`, `decision_quorum_already_met`.
- Per-route token-bucket rate limit (existing #111 mechanism).

**DoD**: handler-level table tests for every endpoint × every closed-enum code path.

### Task 8 — Integration test (commit 8)

- `plane/application/policy/policy_integration_test.go` — Postgres testcontainer + Temporal testcontainer.
- Scenarios: (a) submit → approve happy path; (b) submit → SLA breach → escalate to next rung → approve; (c) all rungs exhausted with `auto_deny` → rejected; (d) plan-hash mutation re-opens approval; (e) non-approver decision rejected with `decision_unauthorized`; (f) `auto_approved_no_rule` audit emitted when no rule matches.
- Each scenario asserts outbox rows + Merkle chain integrity end-to-end.

**DoD**: integration test green in CI; `go test -race ./plane/application/policy/...` clean.

### Task 9 — Architecture lint coverage (commit 9)

- Extend `internal/architecture/` lint to assert `plane/workflow/approval/` does not reach `Transact*`/`WriteOutbox*`/`Insert*`/`Update*`/`Delete*` via any import path.
- Negative test: temporarily import `MetadataStore` in `approval/activity.go` and confirm lint fails; revert.

**DoD**: lint passes on clean tree; lint fails on intentional violation in negative test.

### Task 10 — Migration of billing DEK-destruction approval (follow-up, deferred)

Register a follow-up issue at PR-time:
> "Migrate `plane/workflow/billing/RequestOperatorApprovalActivity` callers to generic `plane/workflow/approval/ApprovalActivity`; remove deprecated stub once last caller migrates."

Do NOT migrate in this PR — keeps blast radius tight.

## Pre-push gate

Run on the worktree before `gh pr create`:

```
go build ./...
go vet ./...
golangci-lint run ./...
go test -race ./plane/application/policy/... ./plane/workflow/approval/...
make lint-events
make lint-determinism
make lint-md
```

All must be green.

## Self-review battery (parallel, before `gh pr create`)

- `pr-review-toolkit:code-reviewer`
- `pr-review-toolkit:silent-failure-hunter`
- `pr-review-toolkit:type-design-analyzer` (new public types: `Policy`, `Rule`, `PredicateKind`, `ApproverGroup`, `EscalationRung`, `Decision`, `PolicyStore`, `Engine`, `ApprovalActivity`)
- `pr-review-toolkit:pr-test-analyzer`
- `pr-review-toolkit:comment-analyzer` (new doc comments throughout)
- `adr-historian` (touches ADR-015, ADR-019, ADR-008, ADR-017)
- `comprehensive-review:architect-review` (plane boundary + new Kafka topics)

Resolve every actionable finding before final commit. Record pass in PR self-review block.

## PR

- Title: `[Application] Plan-approval full policy DSL + escalation (ADR-019)`
- Branch: `feat/application-plan-approval-policy-dsl`
- Body sections: Summary (≤4 bullets), ADR-impact (`conforming` to ADR-015 / ADR-019 / ADR-008 / ADR-017), Test plan (acceptance-criteria checklist from #115), `Closes #115`, spec + plan cross-links, self-review block (collapsed), co-author trailer.
- Commit convention: Conventional Commits with plane scope, e.g. `feat(application/policy): plan-approval engine + Merkle audit (#115)`.

## Out-of-scope reminders

- No ML risk classification.
- No cross-org policy templates.
- No approval-delegation v2 features.
- No migration of billing DEK-destruction stub (follow-up).
- No mobile-UX work.

## Risks

- **HMAC key rotation**: org KEK rotation must re-sign all org policies. Out of scope here; document in PR follow-up.
- **Long-poll deadline tuning**: 5s jitter is a guess; revisit after first design-partner trial.
- **Merkle chain contention**: `FOR UPDATE` on the latest row serializes audit appends per policy. Acceptable at expected rates (≤10 decisions/sec/policy); revisit if concurrent-policy throughput becomes a bottleneck.
