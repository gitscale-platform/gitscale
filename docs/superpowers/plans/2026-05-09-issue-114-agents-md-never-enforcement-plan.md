# Issue #114 AGENTS.md surfacing + `Never` enforcement — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `NoopHookHandler` with an `AgentsMdHookHandler` that parses `AGENTS.md`, merges org+repo policy (org wins), and hard-rejects pushes that match a `## Never` predicate. Ship the parser as a public Go API consumable by the MCP server (#112).

**Architecture:** Three-package layout — `plane/application/agentsmd` (parser + evaluator, pure), `plane/application/agentsmd/hook` (adapter implementing `plane/git.HookHandler`), `plane/application/agentsmd/policystore` (org-policy resolver atop `application.Service`). Production `BlobReader` lives in `plane/git/gitaly` to keep imports one-way.

**Tech Stack:** Go 1.22, stdlib `encoding/yaml` via `gopkg.in/yaml.v3` (already in deps), `path/filepath` glob, `google/uuid`, testcontainers-go for Gitaly + Postgres.

**Spec:** `docs/superpowers/specs/2026-05-09-issue-114-agents-md-never-enforcement-design.md`

**Branch:** `feat/application-agents-md-never-enforcement` (worktree: `../gitscale.worktrees/feat-application-agents-md-never-enforcement`)

**ADR-impact:** conforming (ADR-008 outbox neutral, ADR-012 hook layer, ADR-019 plane boundary). No new ADR.

**Closes:** #114

**Depends on:** #107 merged (provides `plane/git.HookHandler` interface, `RepoRef`, `RefUpdate`, the proxy construction site, and the testcontainers Gitaly fixture).

---

## File map

### Create — `plane/application/agentsmd/`

- [ ] `doc.go` — package doc, ADR refs, plane-boundary statement (no `plane/git` imports).
- [ ] `policy.go` — `Policy`, `NeverPredicate`, `PredicateName` enum, `PredicateSelector`.
- [ ] `parser.go` — `Parse([]byte) (*Policy, []Diagnostic, error)`.
- [ ] `parser_test.go` — front-matter, `## Never` extraction, version checks, empty-input.
- [ ] `lint.go` — `Lint([]byte) []Diagnostic` (re-uses parser, returns diagnostics only).
- [ ] `lint_test.go` — every diagnostic code emitted on representative inputs.
- [ ] `merge.go` — `Merge(org, repo *Policy) *Policy`.
- [ ] `merge_test.go` — org-wins on conflict, predicate-key dedup, union order.
- [ ] `evaluate.go` — `Evaluate(*Policy, EvaluationInput) []Violation`, plus `FileResolver` interface, `RefUpdate` struct (mirrors `plane/git.RefUpdate` shape but copied to avoid the import).
- [ ] `evaluate_test.go` — each predicate kind, glob edge cases, lazy file-list resolver invoked only when needed.
- [ ] `diagnostic.go` — `Diagnostic`, `Severity` type, code constants.

### Create — `plane/application/agentsmd/hook/`

- [ ] `doc.go` — adapter package, allowed imports (`plane/git`, `agentsmd`, `agentsmd/policystore`).
- [ ] `blob_reader.go` — `BlobReader` interface (production impl is in `plane/git/gitaly`).
- [ ] `handler.go` — `Handler` struct, `New(parser, blobs, policies) *Handler`, `PreReceive(...)`.
- [ ] `handler_test.go` — in-memory `BlobReader` + stub `PolicyStore`; rejection paths; gRPC code mapping.
- [ ] `mem_blob_reader_test.go` — test helper, `_test.go` suffix.

### Create — `plane/application/agentsmd/policystore/`

- [ ] `doc.go` — package doc.
- [ ] `policystore.go` — `PolicyStore` interface, `ServicePolicyStore` impl wrapping `application.Service`.
- [ ] `policystore_test.go` — testcontainers Postgres + real service; repo-not-found, org-not-found, blob-missing, blob-found.

### Create — `plane/application/agentsmd/`

- [ ] `integration_test.go` — testcontainers Gitaly + Postgres; push violating `Never` rejected, clean push accepted, malformed AGENTS.md does not block pushes (lint diagnostic surfaced via parser only).

### Create — `plane/git/gitaly/blob_reader.go` (extension to #107)

- [ ] `blob_reader.go` — `GitalyBlobReader` implementing the adapter package's `BlobReader` interface using Gitaly `GetBlobByPath` + `GetTreeEntries` RPCs.
- [ ] `blob_reader_test.go` — testcontainers Gitaly; round-trips on a fixture repo.

### Modify — `cmd/git-rpc/main.go` (or whichever binary #107 produces)

- [ ] Replace `hook.NoopHookHandler{}` construction with `agentsmdhook.New(...)` wired to `agentsmd.NewParser()`, `gitaly.NewBlobReader(pool)`, and `policystore.NewServicePolicyStore(applicationService)`.
- [ ] Add `--agents-md-enforcement=on|off` flag (default `on`); `off` reverts to `NoopHookHandler` for emergency bypass. Document in binary `--help`.

### Modify — `docs/architecture.md` (only if no §8 entry exists)

- [ ] No ADR change. If a brief mention of AGENTS.md enforcement is missing from §9, append a single sentence noting `Never` is hard-blocked at the hook layer (per ADR-012) and the parser is plane/application.

---

## Task sequence

### Task 1 — Parser + diagnostics (pure, no plane boundary)

- [ ] Implement `policy.go`, `diagnostic.go`, `parser.go`, `lint.go`.
- [ ] Hardcode `schema: gitscale/v1` acceptance; emit `unsupported_schema_version` for anything else.
- [ ] Front-matter via `yaml.v3`; body Markdown scanned for `## Never` heading and bullet items of shape `- predicate_name: <selector>`.
- [ ] `parser_test.go`, `lint_test.go` cover every diagnostic code, empty input, malformed YAML, missing `## Never` block, duplicate predicates, unknown predicate name.
- [ ] Run `go test ./plane/application/agentsmd/...`. Commit.

### Task 2 — Evaluator + merge

- [ ] Implement `evaluate.go` with closed-switch over `PredicateName`. Each predicate has a focused matcher fn.
- [ ] `force_push_to_branch`: ref matches `BranchGlob` AND ref already exists AND new OID is not a fast-forward of old OID (heuristic: any branch update with non-zero OldOID where NewOID is not ancestor → caller supplies via `FileResolver` or we accept `IsFastForward(ctx, old, new) (bool, error)` on the resolver). Add `IsFastForward` to `FileResolver`.
- [ ] `delete_branch`: ref matches `BranchGlob` AND `NewOID == "0000…"`.
- [ ] `push_to_branch`: ref matches `BranchGlob` (any update).
- [ ] `modify_path`: any changed path matches `PathGlob` (lazy `Changed()` call).
- [ ] `push_binary_over_size`: any changed-blob `Size()` > `MaxBytes`.
- [ ] `merge.go` per spec (org wins on duplicate predicate-keys; key = name + selector hash).
- [ ] `evaluate_test.go`, `merge_test.go`. Commit.

### Task 3 — Hook adapter

- [ ] Implement `hook/blob_reader.go` interface, `hook/handler.go` `New` + `PreReceive`.
- [ ] `PreReceive` flow: resolve org policy via `PolicyStore`; read repo `AGENTS.md` via `BlobReader.ReadBlob(ctx, repo.RepoID, "HEAD", "AGENTS.md")` (use the new tip OID of the default branch update if present, else `HEAD`); parse both; merge; build `EvaluationInput` with a `FileResolver` backed by `BlobReader`; if any `Violation`, return error.
- [ ] Error message format: `"AGENTS.md Never violation: <name> on ref <ref> (<reason>)"`. Multiple violations → join with `; `.
- [ ] Unit tests with in-memory `BlobReader` and stub `PolicyStore`. Commit.

### Task 4 — Policy store (testcontainers Postgres)

- [ ] Implement `policystore/policystore.go`. `ResolveOrgPolicy(ctx, repoID)` → `application.Service.GetRepo(ctx, repoID)` → org_id → query for `<org>/.gitscale-agents` repo → call `BlobReader.ReadBlob(ctx, agentsRepoID, "HEAD", "AGENTS.md")`.
- [ ] `BlobReader` is injected (test in-memory; production GitalyBlobReader from §gitaly).
- [ ] `policystore_test.go` exercises every branch.

### Task 5 — Production `BlobReader` in `plane/git/gitaly`

- [ ] Implement `GitalyBlobReader` using `pool.Conn(server) → gitalypb.NewBlobServiceClient` + `NewCommitServiceClient`.
- [ ] `ReadBlob`, `ListChangedPaths`, `Size`, `IsFastForward` (latter via `CommitService.CheckObjectsExist` + `IsAncestor`).
- [ ] testcontainers Gitaly fixture-repo round-trips. Commit.

### Task 6 — Integration test

- [ ] `agentsmd/integration_test.go`: spin testcontainers Gitaly + Postgres; seed a repo with an `AGENTS.md` containing `Never: delete_branch on main`; push a delete of `main` and assert the gRPC error code is `PermissionDenied` with the structured message.
- [ ] Second case: push without violation passes.
- [ ] Third case: malformed AGENTS.md does **not** block a push (per spec error-handling table).

### Task 7 — Wire in production binary

- [ ] Modify `cmd/git-rpc/main.go` to construct `agentsmdhook.New(...)`. Add `--agents-md-enforcement` flag.
- [ ] Smoke test: existing #107 integration test still green; new #114 integration test green.

### Task 8 — Pre-push gates + self-review battery + PR

- [ ] Run pre-push gates locally:
  - `go build ./...`
  - `go vet ./...`
  - `golangci-lint run ./...`
  - `go test -race ./plane/application/agentsmd/... ./plane/git/...`
  - `make lint-events` (no new events; should be no-op)
  - `make lint-determinism`
- [ ] Run mandatory pre-commit skills (per supervisor plan, issue #114 row):
  - `gitscale-adr-guard`
  - `gitscale-go-conventions`
  - `gitscale-plane-boundary`
  - `gitscale-agent-quota-check`
- [ ] Dispatch self-review battery in parallel:
  - `pr-review-toolkit:code-reviewer`
  - `pr-review-toolkit:silent-failure-hunter`
  - `adr-historian`
  - `pr-review-toolkit:type-design-analyzer` (new public types: `Policy`, `NeverPredicate`, `Diagnostic`, `Handler`, `BlobReader`, `PolicyStore`)
  - `pr-review-toolkit:pr-test-analyzer` (always)
  - `pr-review-toolkit:comment-analyzer` (doc comments added)
  - `comprehensive-review:architect-review` (touches `plane/git` ↔ `plane/application` boundary)
- [ ] Resolve every actionable finding before final commit.
- [ ] `gh pr create` with title `[Application] AGENTS.md surfacing + Never enforcement` and body sections per supervisor PR quality bar.

---

## Acceptance criteria (mirrors issue body)

- [ ] Parser extracts `## Never` predicates from AGENTS.md `gitscale/v1` schema.
- [ ] Effective policy merges repo + org policies (org wins on conflict).
- [ ] Pre-receive hook rejects push matching a `Never` predicate with `PermissionDenied` + structured message.
- [ ] Agent push not matching any `Never` predicate passes through.
- [ ] `agents_md_lint` returns structured diagnostics for malformed AGENTS.md.
- [ ] Integration test: push with and without `Never` violation against testcontainers Gitaly + Postgres.

## Plane-boundary checklist

- [ ] `plane/application/agentsmd` imports nothing from `plane/git`.
- [ ] `plane/application/agentsmd/hook` imports `plane/git` for `HookHandler`, `RepoRef`, `RefUpdate` only.
- [ ] `plane/git/gitaly/blob_reader.go` imports the adapter's `BlobReader` interface (one-way).
- [ ] `gitscale-plane-boundary` skill green.

## Open arch deferrals

- AGENTS.md schema versioning policy (July 2026) — parser hardcoded to `gitscale/v1`.
- ML-based vs rule-based reputation (July 2026) — N/A here; no reputation logic in #114.

## Cross-references

- Spec: `docs/superpowers/specs/2026-05-09-issue-114-agents-md-never-enforcement-design.md`
- Issue: https://github.com/gitscale-platform/gitscale/issues/114
- Depends on PR for #107 to be merged first.
