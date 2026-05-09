package hook

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gitscale-platform/gitscale/plane/application/agentsmd"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
	githook "github.com/gitscale-platform/gitscale/plane/git/hook"
)

// PolicyStore resolves the org-level AGENTS.md policy bytes for a given
// repository. A nil/zero-length result means "no org policy"; the
// handler treats that as an empty org policy. Errors are load-bearing —
// the handler fails closed (rejects the push) on store failures.
type PolicyStore interface {
	ResolveOrgPolicyBytes(ctx context.Context, repoID string) ([]byte, error)
}

// AgentsMdPath is the conventional repo-root location of the policy
// document. Always relative to the repo root.
const AgentsMdPath = "AGENTS.md"

// Handler implements githook.HookHandler. It reads the repo's AGENTS.md
// at the tip of each ref update, merges with the org policy resolved by
// PolicyStore, evaluates Never predicates, and rejects on match.
//
// Construction: see New. The zero value is not usable.
type Handler struct {
	blobs    BlobReader
	policies PolicyStore
}

// Compile-time check.
var _ githook.HookHandler = (*Handler)(nil)

// New constructs an enforcement handler. All three arguments must be
// non-nil; passing nil panics at construction time (a programmer error,
// not a runtime configuration issue).
func New(blobs BlobReader, policies PolicyStore) *Handler {
	if blobs == nil {
		panic("agentsmd/hook: nil BlobReader")
	}
	if policies == nil {
		panic("agentsmd/hook: nil PolicyStore")
	}
	return &Handler{blobs: blobs, policies: policies}
}

// PreReceive evaluates Never predicates against the pushed updates.
// Behaviour summary (mirrors the spec error-handling table):
//
//   - Repo AGENTS.md missing -> empty repo policy (no rejection).
//   - Repo AGENTS.md malformed -> push allowed; the push doesn't get
//     bricked by parse errors. The diagnostics are not currently
//     surfaced from the hook — the MCP `agents_md_lint` tool is the
//     surface for those.
//   - BlobReader infra error -> push rejected (caller wraps as Unavailable).
//   - PolicyStore error -> push rejected.
//   - Any Never predicate matches -> push rejected with a structured
//     single-line message. Multiple violations are joined by "; ".
func (h *Handler) PreReceive(ctx context.Context, repo gittypes.RepoRef, updates []gittypes.RefUpdate) error {
	repoBytes, err := h.readRepoPolicy(ctx, repo, updates)
	if err != nil {
		return fmt.Errorf("agentsmd: read repo policy: %w", err)
	}
	orgBytes, err := h.policies.ResolveOrgPolicyBytes(ctx, repo.RepoID)
	if err != nil {
		return fmt.Errorf("agentsmd: resolve org policy: %w", err)
	}

	repoPolicy, _, _ := agentsmd.Parse(repoBytes)
	orgPolicy, _, _ := agentsmd.Parse(orgBytes)
	merged := agentsmd.Merge(orgPolicy, repoPolicy)
	if merged.Empty {
		return nil
	}

	input := agentsmd.EvaluationInput{
		RepoID:  repo.RepoID,
		AgentID: repo.AgentID,
		Updates: convertUpdates(updates),
		Files:   &resolverAdapter{repoID: repo.RepoID, blobs: h.blobs},
	}
	violations, err := agentsmd.Evaluate(ctx, merged, input)
	if err != nil {
		return fmt.Errorf("agentsmd: evaluate: %w", err)
	}
	if len(violations) == 0 {
		return nil
	}
	return formatViolations(violations)
}

// readRepoPolicy fetches AGENTS.md from the repo. If the push includes
// an update to refs/heads/HEAD's default branch, we'd ideally read the
// new tip's AGENTS.md — but the proxy doesn't tell us which ref is the
// default branch. We use a simple heuristic: if any update targets a
// non-zero NewOID, try that OID first; on miss fall back to "HEAD".
// Missing AGENTS.md is not an error — empty bytes signal an empty
// policy to Parse.
func (h *Handler) readRepoPolicy(ctx context.Context, repo gittypes.RepoRef, updates []gittypes.RefUpdate) ([]byte, error) {
	tryOID := func(oid string) ([]byte, bool, error) {
		if oid == "" || oid == agentsmd.ZeroOID {
			return nil, false, nil
		}
		data, err := h.blobs.ReadBlob(ctx, repo.RepoID, oid, AgentsMdPath)
		if err == nil {
			return data, true, nil
		}
		if errors.Is(err, ErrBlobNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// Prefer the new tip; on delete (NewOID == zero), fall back to the
	// pre-push tip so the repo's own policy still gates the delete.
	for _, u := range updates {
		if data, ok, err := tryOID(u.NewOID); err != nil {
			return nil, err
		} else if ok {
			return data, nil
		}
	}
	for _, u := range updates {
		if data, ok, err := tryOID(u.OldOID); err != nil {
			return nil, err
		} else if ok {
			return data, nil
		}
	}
	data, err := h.blobs.ReadBlob(ctx, repo.RepoID, "HEAD", AgentsMdPath)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, ErrBlobNotFound) {
		return nil, nil
	}
	return nil, err
}

func convertUpdates(in []gittypes.RefUpdate) []agentsmd.RefUpdate {
	out := make([]agentsmd.RefUpdate, len(in))
	for i, u := range in {
		out[i] = agentsmd.RefUpdate{RefName: u.RefName, OldOID: u.OldOID, NewOID: u.NewOID}
	}
	return out
}

// resolverAdapter implements agentsmd.FileResolver atop BlobReader.
// Bound to a single repo at construction time so callers don't have to
// thread repoID through every call.
type resolverAdapter struct {
	repoID string
	blobs  BlobReader
}

func (r *resolverAdapter) Changed(ctx context.Context, oldOID, newOID string) ([]string, error) {
	return r.blobs.ListChangedPaths(ctx, r.repoID, oldOID, newOID)
}
func (r *resolverAdapter) Size(ctx context.Context, oid, path string) (int64, error) {
	return r.blobs.BlobSize(ctx, r.repoID, oid, path)
}
func (r *resolverAdapter) IsFastForward(ctx context.Context, oldOID, newOID string) (bool, error) {
	return r.blobs.IsFastForward(ctx, r.repoID, oldOID, newOID)
}

// formatViolations builds the single-line, structured error string. The
// proxy converts this to gRPC PermissionDenied with the message intact.
func formatViolations(vs []agentsmd.Violation) error {
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		parts = append(parts, fmt.Sprintf("AGENTS.md Never violation: %s on ref %s (%s)",
			v.Predicate.Name, v.RefName, v.Reason))
	}
	return errors.New(strings.Join(parts, "; "))
}
