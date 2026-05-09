// Package hook adapts plane/application/agentsmd to the
// plane/git.HookHandler contract. It is the only place in the
// agentsmd subtree that imports plane/git types — keeping the parser
// and evaluator pure (see ADR-019).
//
// Allowed imports:
//   - plane/git/gittypes (RepoRef, RefUpdate)
//   - plane/git/hook (HookHandler interface)
//   - plane/application/agentsmd
//   - plane/application/agentsmd/policystore
//
// Forbidden:
//   - direct Gitaly proto imports — use the BlobReader interface, whose
//     production implementation lives in plane/git/gitaly.
package hook
