# Spec — Issue #114 AGENTS.md surfacing + `Never` enforcement

Date: 2026-05-09
Issue: https://github.com/gitscale-platform/gitscale/issues/114
Planes: application (parser, policy middleware, MCP `agents_md_*` impl) + git (HookHandler wiring)
Priority: p1 (Wave 0)
ADR-impact: conforming (ADR-008 outbox; ADR-019 plane boundary; ADR-012 metering hook layer)

## Problem

`AGENTS.md` is the GitScale mechanism by which humans communicate
constraints to AI agents. The git plane (#107) ships a `NoopHookHandler`
as the default `HookHandler` on `GitalyProxy.PreReceive`. Issue #114
replaces that with an `AgentsMdHookHandler` that:

1. Reads `AGENTS.md` from repo root at the push's tip (per ref update).
2. Merges org-level + repo-level policy (org wins on conflicts).
3. Evaluates each ref update against the `## Never` predicate list.
4. Hard-rejects with `codes.PermissionDenied` and a structured message
   if any predicate matches.

The same parser is exposed to agents via the MCP server (#112) as
`agents_md_get(repo_id)` and `agents_md_lint(content)`; #114 ships the
parser and its public API, #112 wires the MCP tool to it.

## Goals

1. Add `plane/application/agentsmd` package owning parser, schema, and
   in-process policy evaluator. Public types: `Policy`, `NeverPredicate`,
   `Diagnostic`, `Severity`, plus `Parse([]byte) (*Policy, []Diagnostic, error)`,
   `Lint([]byte) []Diagnostic`, `Merge(org, repo *Policy) *Policy`,
   `Evaluate(*Policy, EvaluationInput) []Violation`.
2. Add `plane/application/agentsmd/hook` package — adapter
   implementing `git.HookHandler` from §3 of the git plane spec. Reads
   `AGENTS.md` blobs via an injected `BlobReader` interface (production
   impl backed by Gitaly `GetBlobByPath`, test impl in-memory).
3. Add `plane/application/agentsmd/policystore` — minimal store that
   resolves the org-level policy by `repo_id` (uses existing repository
   metadata service to map repo → org → org policy blob path).
4. Wire `AgentsMdHookHandler` into the `cmd/git-rpc` (or `cmd/gitaly-proxy`)
   binary as the default `HookHandler` replacement, behind a single
   construction site. `NoopHookHandler` remains the testing default but
   is not used by production binaries.
5. `agents_md_lint` returns structured diagnostics with stable codes:
   `unknown_section`, `malformed_predicate`, `unsupported_schema_version`,
   `duplicate_predicate`, `empty_never_block`.
6. Integration test against testcontainers Gitaly + Postgres performs a
   push that includes a `Never`-violating ref update and verifies it is
   rejected with the structured message.

## Non-goals

- MCP server tool wiring (`agents_md_get` / `agents_md_lint` over MCP)
  — that lives in #112; this issue ships the underlying Go API.
- `## Always` / `## Prefer` block enforcement — schema parses and
  surfaces them, but only `Never` is hard-blocked. Other blocks are
  advisory in this issue.
- AGENTS.md schema versioning policy (open question, July 2026). The
  parser is **version-aware**: it reads the front-matter `schema:` field,
  rejects unknown majors with `unsupported_schema_version`, but pinning
  vs. tracking upstream is deferred. We hardcode acceptance of
  `gitscale/v1` only.
- Per-org override beyond `org > repo`. No team-level or branch-protection
  level overrides in this issue.
- HTTP / GraphQL surface for AGENTS.md — exposure is via MCP only (#112)
  and the pre-receive hook.
- Cross-org policy inheritance — feature-flagged off (per scope guardrails).

## Design decisions (defaults selected by supervisor — auto-mode)

| Question | Choice | Rationale |
|---|---|---|
| Schema format | YAML front-matter (`---` block) for `schema:` + `version:` + structured policy fields, then Markdown body for human prose. `## Never` block extracted from Markdown body, not front-matter. | Matches conventional AGENTS.md ecosystem; lets humans write prose freely; structured fields stay machine-readable. |
| `Never` predicate vocabulary | Closed enum v1: `force_push_to_branch`, `delete_branch`, `push_to_branch`, `modify_path`, `push_binary_over_size`. Each predicate has a fixed shape (regex / glob / int). Unknown predicate name → `Diagnostic` of severity `error`, predicate skipped (not enforced). | Closed enum is exhaustively switchable in the evaluator; unknown predicates fail closed at lint time but cannot silently bypass enforcement at runtime (skipped predicates do not match). Documented as such in the spec. |
| Predicate matching scope | Per `RefUpdate`. The hook iterates updates and evaluates predicates with `(RefName, OldOID, NewOID, RepoID, AgentID)` plus a lazy file-list resolver (Gitaly `GetTreeEntries` between OldOID and NewOID). | File-list resolution is lazy because most pushes don't need it; `force_push_to_branch` and `delete_branch` need only ref-level inputs. |
| `BlobReader` boundary | Defined in `agentsmd/hook` as `BlobReader { ReadBlob(ctx, repoID, ref, path) ([]byte, error); ListChangedPaths(ctx, repoID, oldOID, newOID) ([]string, error) }`. Production impl in `plane/git/gitaly` package wraps Gitaly RPCs. Test impl in package. | Keeps the parser+evaluator package free of Gitaly imports (plane boundary); satisfies `gitscale-plane-boundary` skill. |
| Policy cache | None in this issue. Each push reads the AGENTS.md blob via Gitaly. `BlobReader` impl can layer caching later. | Avoids cache-coherence bugs in the load-bearing enforcement path; latency budget for pre-receive is generous because pushes are relatively rare per agent. |
| Org-policy lookup | `policystore.ResolveOrgPolicy(ctx, repoID) ([]byte, error)` — uses existing repository metadata service (`Service.GetRepo` → org_id) and reads `AGENTS.md` from a conventional `agents-policy` repo named `<org>/.gitscale-agents`. If the repo or file does not exist, returns `nil, nil` (no org policy). | Convention-over-config; matches how `.github/` repos work in GitHub. Avoids new metadata schema. |
| Merge semantics | `Merge(org, repo)` returns a new `Policy`. Predicates union; on duplicate predicate keys (same predicate name + same selector), `org` wins. Order in the result: org predicates first, then repo predicates that didn't conflict. | "Org overrides on conflict" per issue body; predicate-key deduplication is exhaustive. |
| Diagnostic stability | `Diagnostic.Code` is a closed string enum (the codes listed in goal #5). `Severity` is `error` or `warning`. Stable across versions. | MCP `agents_md_lint` clients (humans and IDEs) need stable codes. |
| Violation message format | `"AGENTS.md Never violation: <predicate_name> on ref <ref> (<reason>)"` — single line, structured prefix, deterministic. | Surfaced to the Git client via gRPC `PermissionDenied` message; needs to be greppable in CI logs. |
| Schema-version policy | Hardcode `schema: gitscale/v1` acceptance. Unknown versions emit `unsupported_schema_version` diagnostic at lint time and the parser returns `nil` policy. | Open arch question (July 2026) deferred; we ship enforcement now, version-tracking later. |
| Empty / missing AGENTS.md | `Parse(nil)` returns `(*Policy{Empty: true}, nil, nil)`; `Evaluate` over an empty policy returns no violations. The hook short-circuits when `Policy.Empty && OrgPolicy.Empty`. | Repos without AGENTS.md must be unaffected. |
| Test backend | Existing testcontainers Gitaly + Postgres pattern from #107; in-package unit tests use the in-memory `BlobReader`. No mock-DB tests for the integration suite. | Mirrors plane invariant: real-store integration tests, in-memory units. |

## Public types

```go
package agentsmd

type Policy struct {
    SchemaVersion string
    Empty         bool
    Never         []NeverPredicate
}

type NeverPredicate struct {
    Name     PredicateName // closed enum
    Selector PredicateSelector
}

type PredicateName string

const (
    PredicateForcePushToBranch  PredicateName = "force_push_to_branch"
    PredicateDeleteBranch       PredicateName = "delete_branch"
    PredicatePushToBranch       PredicateName = "push_to_branch"
    PredicateModifyPath         PredicateName = "modify_path"
    PredicatePushBinaryOverSize PredicateName = "push_binary_over_size"
)

type PredicateSelector struct {
    BranchGlob   string // for *_branch predicates
    PathGlob     string // for modify_path
    MaxBytes     int64  // for push_binary_over_size
}

type Diagnostic struct {
    Code     string
    Severity Severity // "error" | "warning"
    Line     int
    Message  string
}

type Severity string

type EvaluationInput struct {
    RepoID    string
    AgentID   string
    Updates   []RefUpdate // mirrors plane/git.RefUpdate shape
    Files     FileResolver
}

type FileResolver interface {
    Changed(ctx context.Context, oldOID, newOID string) ([]string, error)
    Size(ctx context.Context, oid string) (int64, error)
}

type Violation struct {
    Predicate NeverPredicate
    RefName   string
    Reason    string
}
```

```go
package hook // plane/application/agentsmd/hook

type Handler struct {
    parser     *agentsmd.Parser
    blobs      BlobReader
    policies   PolicyStore
}

func (h *Handler) PreReceive(ctx context.Context, repo git.RepoRef, updates []git.RefUpdate) error
```

## Integration with #107 hook layer

`cmd/git-rpc` (or `cmd/gitaly-proxy` — name follows the binary chosen
by #107) construction site replaces:

```go
proxy := proxy.New(pool, locator, hook.NoopHookHandler{}, counter)
```

with:

```go
agentsHook := agentsmdhook.New(parser, gitaly.NewBlobReader(pool), policyStore)
proxy := proxy.New(pool, locator, agentsHook, counter)
```

`NoopHookHandler` remains in `plane/git/hook/` for tests.

## Plane boundary

- `plane/application/agentsmd` — parser, policy types, evaluator. No
  imports from `plane/git/...`. Pure logic.
- `plane/application/agentsmd/hook` — adapter package. Imports
  `plane/git` for the `HookHandler` interface and `RepoRef`/`RefUpdate`
  types only. Imports `agentsmd` for the policy types.
- `plane/git/gitaly` (extension to #107) — `BlobReader` production
  impl. Imports Gitaly proto + `agentsmd/hook.BlobReader` interface.
- `plane/application/agentsmd/policystore` — uses existing
  `application.Service` (repo-by-id), no direct DB access.

The `gitscale-plane-boundary` skill must pass: no cross-plane imports
beyond the adapter package.

## Outbox + events

No new outbox events from this issue. AGENTS.md violations are
synchronous push rejections — the audit signal is the rejected push,
which the metering layer (#107) already records as a hook-rejected
push. Adding `agents_md_violated` events is a follow-up.

## Error handling

| Condition | Behaviour |
|---|---|
| `AGENTS.md` missing in repo | Treated as empty policy. No rejection. |
| `AGENTS.md` malformed (parse error) | Push **allowed**. `Diagnostic.Severity=error` is logged but not enforced. The MCP `agents_md_lint` tool surfaces the diagnostics to humans; we do not block pushes on parse failures because that would brick the repo. |
| `BlobReader` Gitaly RPC fails | Push **rejected** with `Unavailable`. AGENTS.md enforcement is load-bearing per the issue body; we fail closed on infra errors. |
| `policystore.ResolveOrgPolicy` fails | Push **rejected** with `Unavailable`. |
| Predicate matches `Never` | Push **rejected** with `PermissionDenied` and structured message. |
| Unknown predicate in policy | Lint diagnostic at parse time; predicate skipped at evaluation (not enforced, not matched). |

## Testing strategy

| Test file | Backend | Coverage |
|---|---|---|
| `agentsmd/parser_test.go` | none | YAML front-matter parsing, `## Never` block extraction, schema version checks, empty-input handling. |
| `agentsmd/evaluate_test.go` | in-memory `FileResolver` | Each predicate kind matches and non-matches; selector glob edge cases. |
| `agentsmd/merge_test.go` | none | Org wins on conflict, union semantics, predicate-key dedup. |
| `agentsmd/lint_test.go` | none | All five diagnostic codes emitted on representative inputs. |
| `agentsmd/hook/handler_test.go` | in-memory `BlobReader` + stub `PolicyStore` | Hook calls parser + evaluator; rejection paths; error-mapping to gRPC codes. |
| `agentsmd/policystore/policystore_test.go` | testcontainers Postgres (real `application.Service`) | `ResolveOrgPolicy` repo-not-found, org-not-found, blob-missing, blob-found. |
| `agentsmd/integration_test.go` | testcontainers Gitaly + Postgres | End-to-end push that violates `Never` is rejected; push that does not is accepted. |

GA gate: `make test` passes with no mock-DB tests in `agentsmd/policystore`
or `agentsmd/integration_test.go`.

## Out of scope follow-ups (file as new issues)

- `agents_md_violated` outbox event for analytics (deferred — file if
  product needs analytics on violation rates).
- Schema versioning policy (open arch question, July 2026).
- `## Always` / `## Prefer` enforcement (deferred; warnings only here).
- Cross-org policy inheritance (feature-flagged off).
- Policy cache layer (re-add when latency profile shows the lookup
  dominating push latency).

## Cross-references

- Issue: https://github.com/gitscale-platform/gitscale/issues/114
- Spec (git plane foundation): `docs/superpowers/specs/2026-05-09-git-plane-foundation-design.md`
- Plan (git plane foundation): `docs/superpowers/plans/2026-05-09-git-plane-foundation.md`
- ADR-012 (two-tier metering): `docs/architecture.md §8`
- ADR-019 (workflow→application boundary): `docs/superpowers/specs/2026-05-06-adr-019-workflow-app-plane-boundary.md`
