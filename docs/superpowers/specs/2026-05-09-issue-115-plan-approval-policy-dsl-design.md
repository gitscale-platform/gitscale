# Design — Issue #115: Plan-approval full policy DSL + escalation (ADR-019)

- Run: 2026-05-09 supervisor
- Issue: [#115] [Application/Workflow] Plan-approval full policy DSL + escalation (ADR-019)
- Governing ADRs: ADR-015 (plan-approval policy model), ADR-019 (workflow→application RPC boundary), ADR-008 (outbox), ADR-017 (Go interface swap surfaces)
- Predecessors: #74 billing gRPC service (appclient pattern), #18-archive billing approval stub (`plane/workflow/billing/request_operator_approval_activity.go`)

## Goal

Promote the existing billing-domain operator-approval stub into a general-purpose, org-configurable plan-approval policy engine. Agents submit plans; the application plane evaluates policy predicates, routes to approver groups, blocks the plan via a Temporal `ApprovalActivity` until decision or SLA breach, then escalates per a configured ladder, with every event written to a Merkle-chained audit log.

This is the **safety boundary between agent autonomy and human oversight**. Without it, agent SDK "ask first" hints have no enforcement.

## Non-goals (this issue)

- ML-based risk classification — ADR-015 explicitly defers; rule-based predicates only.
- Cross-org policy templates / marketplace — out of scope; org-local policies only.
- Approval-from-mobile UX — REST + audit log only; UI tooling is downstream.
- Auto-approve defaults — ADR-015 forbids platform-default `auto_approve`. Absence of matching rule = explicit `auto-approve-no-rule` audit row, never silent.
- Cross-plane orchestration beyond ADR-019 boundary — workflow plane is read+long-poll only; never `Transact*`/`WriteOutbox*` directly for policy state.

## Architecture

### Plane ownership (per ADR-019)

| Concern | Plane | Module |
|---|---|---|
| Policy CRUD + storage | `application` | `plane/application/policy/` |
| Plan submission + predicate evaluation | `application` | `plane/application/policy/engine.go` |
| Decision recording + Merkle audit | `application` | `plane/application/policy/audit.go` |
| Outbox + transactional writes | `application` | uses `plane/data/store.MetadataStore.Transact` |
| `ApprovalActivity` (Temporal) | `workflow` | `plane/workflow/approval/` |
| gRPC boundary | both | `plane/workflow/appclient/policy.go` ↔ `plane/application/policy/grpc.go` |
| Schema | `data` | `plane/data/schema/migrations/NNNN_policies.sql` |

Workflow activities never call `MetadataStore.Transact`, `WriteOutbox`, or any `Insert*`/`Update*`/`Delete*` directly for policy state. The static lint in `internal/architecture/` already enforces this; no carve-out applies because every approval emits an outbox row.

### Schema (data plane, `application` schema domain)

```sql
-- application.policies — signed Policy objects per org/repo
CREATE TABLE application.policies (
  id              UUID PRIMARY KEY,
  org_id          UUID NOT NULL REFERENCES identity.orgs(id),
  repo_id         UUID NULL REFERENCES repositories.repos(id), -- NULL = org-level
  name            TEXT NOT NULL,
  version         INT  NOT NULL DEFAULT 1,
  body_json       JSONB NOT NULL,        -- canonical Policy DSL
  signature_hmac  BYTEA NOT NULL,        -- HMAC-SHA256 over canonical body_json with org KEK
  created_by      UUID NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at      TIMESTAMPTZ NULL,
  UNIQUE (org_id, repo_id, name, version)
);

-- application.plans — submitted plans awaiting approval
CREATE TABLE application.plans (
  id            UUID PRIMARY KEY,
  org_id        UUID NOT NULL,
  repo_id       UUID NULL,
  policy_id     UUID NOT NULL REFERENCES application.policies(id),
  proposer_id   UUID NOT NULL,           -- principal_id of the agent
  proposer_kind TEXT NOT NULL CHECK (proposer_kind IN ('agent','service')),
  plan_hash     BYTEA NOT NULL,          -- SHA-256 of canonical action list
  actions_json  JSONB NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','expired','escalated','auto_approved_no_rule','auto_denied')),
  current_rung  INT NOT NULL DEFAULT 0,
  sla_deadline  TIMESTAMPTZ NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  decided_at    TIMESTAMPTZ NULL
);

-- application.policy_audit — Merkle-chained audit log per policy
CREATE TABLE application.policy_audit (
  id           BIGSERIAL PRIMARY KEY,
  policy_id    UUID NOT NULL REFERENCES application.policies(id),
  plan_id      UUID NULL REFERENCES application.plans(id),
  event_kind   TEXT NOT NULL,            -- 'submitted','approved','rejected','escalated','expired','auto_approved_no_rule'
  actor_id     UUID NULL,                -- HumanUser for decisions; NULL for system events
  actor_kind   TEXT NOT NULL,            -- 'human','agent','service'
  payload_json JSONB NOT NULL,
  prev_hash    BYTEA NOT NULL,           -- SHA-256 of previous row's full canonical form (genesis = 32 zero bytes)
  row_hash     BYTEA NOT NULL,           -- SHA-256 over (prev_hash || canonical(payload_json) || actor_id || event_kind || created_at)
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (policy_id, id)
);
CREATE INDEX policy_audit_chain ON application.policy_audit(policy_id, id);
```

Outbox emission: every `policies` write, every `plans` status transition, every `policy_audit` row → outbox event in the same Tx. Event kinds: `policy.created`, `policy.updated`, `policy.deleted`, `plan.submitted`, `plan.approved`, `plan.rejected`, `plan.escalated`, `plan.expired`, `plan.auto_approved_no_rule`, `plan.auto_denied`.

### Policy DSL (Go types, JSON-serializable)

```go
// plane/application/policy/dsl.go

type Policy struct {
    ID            uuid.UUID         `json:"id"`
    OrgID         uuid.UUID         `json:"org_id"`
    RepoID        *uuid.UUID        `json:"repo_id,omitempty"`
    Name          string            `json:"name"`
    Version       int               `json:"version"`
    Rules         []Rule            `json:"rules"`           // first-match wins, lexical order
    ApproverGroups map[string]ApproverGroup `json:"approver_groups"`
}

// PredicateKind is a CLOSED enum. Adding a kind requires migration + ADR amendment.
type PredicateKind string
const (
    PredicatePRMerge          PredicateKind = "pr_merge"
    PredicateForcePush        PredicateKind = "force_push"
    PredicateProductionDeploy PredicateKind = "production_deploy"
    PredicateBulkAction       PredicateKind = "bulk_action"
    PredicateAgentDefault     PredicateKind = "agent_default"  // catch-all for AGENTS.md "ask first"
)

type Rule struct {
    Kind      PredicateKind     `json:"kind"`
    Match     map[string]string `json:"match,omitempty"`     // e.g. {"branch":"main"} for pr_merge
    Threshold *int              `json:"threshold,omitempty"` // e.g. 50 for bulk_action
    Ladder    []EscalationRung  `json:"ladder"`              // ordered, ≥1 rung required
    ExpirySeconds int           `json:"expiry_seconds"`      // approval validity window
}

type ApproverGroup struct {
    Name           string      `json:"name"`
    HumanUserIDs   []uuid.UUID `json:"human_user_ids"`      // ADR-015: HumanUser principals only
    RequiredCount  int         `json:"required_count"`      // k-of-n
}

type EscalationRung struct {
    GroupName    string `json:"group_name"`
    SLASeconds   int    `json:"sla_seconds"`
    OnTimeout    string `json:"on_timeout"`                 // 'notify_next' | 'auto_deny' | 'fall_back'
}
```

DSL validation occurs at `POST /v1/orgs/{org}/policies` and at every read (defence-in-depth). Validation errors are 400 with closed-enum codes (`policy_invalid_predicate_kind`, `policy_empty_ladder`, `policy_unknown_approver_group`, `policy_zero_required_count`, `policy_non_human_approver`).

### Engine evaluation

```
SubmitPlan(plan) → Decision
1. Load Policy by (org_id, repo_id || nil) + verify HMAC.
2. Compute plan_hash = SHA-256(canonical_json(plan.actions)).
3. For each rule in policy.rules (lexical order):
     if predicate.matches(plan.actions, rule.match, rule.threshold):
         return Decision{ rule, ladder: rule.ladder, expiry: rule.expiry_seconds }
4. No match: emit policy_audit row event_kind='auto_approved_no_rule', return Decision{auto: true}.
```

`auto_approved_no_rule` ≠ silent approval. Every such case is audited; ops dashboards alert when rate exceeds 5%/hour (signal that policy is mis-configured).

### ApprovalActivity (workflow plane)

```go
// plane/workflow/approval/activity.go
type ApprovalActivity struct {
    client appclient.PolicyClient // gRPC into plane/application/policy
}

// Execute submits the plan, then long-polls the application plane for the
// decision via WaitForDecision (server-streaming gRPC with deadline = current
// rung's SLA + jitter). On SLA breach, calls EscalateRung; on final rung
// breach with on_timeout='auto_deny', returns rejected.
func (a *ApprovalActivity) Execute(ctx context.Context, in PlanInput) (Decision, error) { ... }
```

ApprovalActivity is registered on every workflow queue that needs gating. Existing billing `RequestOperatorApprovalActivity` is preserved for backwards compatibility but marked deprecated; #115 ships migration of the DEK-destruction workflow to the generic activity in a follow-up issue (registered at PR-time).

### REST API (atop #111 router)

| Method | Path | Body | Auth | Notes |
|---|---|---|---|---|
| POST | `/v1/orgs/{org}/policies` | Policy DSL | Org admin | Validates + HMAC-signs + persists |
| GET | `/v1/orgs/{org}/policies` | — | Org member | List, paginated (cursor) |
| GET | `/v1/orgs/{org}/policies/{id}` | — | Org member | |
| PUT | `/v1/orgs/{org}/policies/{id}` | Policy DSL | Org admin | Bumps version |
| DELETE | `/v1/orgs/{org}/policies/{id}` | — | Org admin | Soft-delete |
| POST | `/v1/plans` | `{policy_id, actions[]}` | Agent or service | Returns `plan_id`; ApprovalActivity invokes this server-side from Temporal |
| GET | `/v1/plans/{id}` | — | Plan owner / approver group | |
| POST | `/v1/plans/{id}/decisions` | `{decision: approve\|reject, reason?}` | HumanUser in approver group | Records decision + Merkle audit row |

Error envelope follows #111 closed-enum codes. New codes: `plan_already_decided`, `plan_expired`, `decision_unauthorized` (caller not in approver group), `decision_quorum_already_met`.

### Phase 1→2 gate

Feature flag `policy_engine.full_dsl` (org-scoped, default `false`). When off, REST endpoints return 503 `feature_not_enabled` and the engine falls back to the existing billing-stub semantics. Ops promotes per-org after design-partner trial of a non-trivial policy. The flag uses the existing `application/featureflag` package (assumed; spec #111 wires the flag store).

## Open questions deferred (out of scope this issue)

- Approval delegation (group A may delegate to group B) — defer to v2 DSL.
- Approval expiry refresh on plan mutation — ADR-015 says re-open; current impl rejects; refresh is a v2 concern.
- Audit Merkle root anchoring (e.g. transparency log / cert-transparency) — defer.
- Approval REST endpoints for non-HumanUser principals — ADR-015 forbids; locked.

## ADR impact

- **ADR-015**: conforming. Implements predicate-rule routing, ApproverGroup with required_count, EscalationLadder with `notify`/`auto_deny`/`fall_back`, plan-hash binding, Merkle audit, HumanUser-only approvers, no platform default auto-approve. Defaults: 24h expiry / 4h security-class — security-class detected via the `production_deploy` predicate kind firing.
- **ADR-019**: conforming. Workflow `ApprovalActivity` calls `appclient.PolicyClient` exclusively; no `Transact*` / `WriteOutbox*` reach across the plane boundary. The static lint already covers `plane/workflow/*` → `plane/data/store/*`; this issue adds the same lint coverage to `plane/workflow/approval/` to confirm no `MetadataStore` import is reachable.
- **ADR-008**: conforming. Every state-mutating Tx writes the source row + outbox row in the same Tx. Polling consumer drains as before.
- **ADR-017**: conforming. `PolicyStore` interface in `plane/application/policy/store.go`; PG-backed impl is one of the conforming implementations. Compliance suite at `plane/application/policy/compliance/`.

No new ADR required — ADR-015 + ADR-019 fully cover the design space.

## Test plan

- Unit: DSL validation, predicate matchers (one test per kind), HMAC sign+verify, Merkle chain construction (genesis, append, tamper detection).
- Integration: full submit→approve via Temporal testcontainers + Postgres testcontainer; long-poll signal flow; SLA timeout escalates to next rung; final-rung `auto_deny` rejects; plan mutation re-opens approval; RBAC denies non-approver decision.
- Compliance: `PolicyStore` compliance suite passes for PG impl.
- Lint: architecture lint confirms `plane/workflow/approval/` cannot reach a `Transact*`/`WriteOutbox*`/`Insert*`/`Update*`/`Delete*` method.

## Acceptance criteria mapping

Each acceptance criterion in issue #115 maps to a task in the plan:

| AC | Plan task |
|---|---|
| Policy DSL types defined | Task 1 |
| Plan submission routes correctly | Task 4 |
| ApprovalActivity blocks + escalates | Task 6 |
| All rungs exhausted → auto-reject | Task 6 |
| Audit chain in outbox with required fields | Task 5 |
| Integration test full flow | Task 8 |
