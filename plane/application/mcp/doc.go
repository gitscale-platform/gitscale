// Package mcp implements the Model Context Protocol server for the
// GitScale application plane (issue #112). It exposes seven tools to
// agent clients (git_clone, pr_create, ci_trigger, quota_status,
// agents_md_get, agents_md_validate, agents_md_evaluate) over an
// HTTP+JSON transport, reusing the REST middleware chain from
// plane/application/restapi and the AGENTS.md parser/evaluator from
// plane/application/agentsmd.
//
// Plane boundary (ADR-019):
//
//	allowed:
//	  - plane/application/restapi          (middleware + router for in-process loopback)
//	  - plane/application/identity         (Service.MintCloneToken)
//	  - plane/application/repositories     (Service.GetRepository)
//	  - plane/application/agentsmd         (Parse, Lint, Evaluate, Merge)
//	  - plane/application/agentsmd/policystore (org-policy resolution)
//	  - plane/application/agentsmd/hook    (BlobReader interface only)
//	  - plane/data/ratelimit               (Inspect + SurfaceMCP)
//	  - go.temporal.io/sdk/client          (cirunclient only)
//
//	forbidden:
//	  - plane/git/...        — blob reads route through agentsmd's BlobReader
//	  - plane/workflow/...   — Temporal SDK client only, no workflow-definition imports
//	  - plane/edge/...
//
// MCP protocol version: open architecture question (target July 2026,
// see CLAUDE.md "Open architecture questions"). The implementation
// surfaces the configured version verbatim from `initialize`; the
// default value is plumbed via Config.ProtocolVersion and a WARN log
// fires at boot if the value equals DeferredDefaultProtocolVersion.
//
// References:
//
//   - Issue: https://github.com/gitscale-platform/gitscale/issues/112
//   - Spec:  docs/superpowers/specs/2026-05-09-issue-112-mcp-server-design.md
//   - Plan:  docs/superpowers/plans/2026-05-09-issue-112-mcp-server-plan.md
//   - ADR-008 outbox; ADR-009 cache; ADR-010 SVID; ADR-012 metering;
//     ADR-017 swap surfaces; ADR-019 plane boundary.
package mcp
