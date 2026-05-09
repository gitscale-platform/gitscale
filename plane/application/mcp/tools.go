package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/application/agentsmd"
	"github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
	"github.com/gitscale-platform/gitscale/plane/application/agentsmd/policystore"
	"github.com/gitscale-platform/gitscale/plane/application/mcp/cirunclient"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	"github.com/google/uuid"
)

// Tool names. Closed enum; adding a value is a public-API change.
const (
	ToolGitClone         = "git_clone"
	ToolPRCreate         = "pr_create"
	ToolCITrigger        = "ci_trigger"
	ToolQuotaStatus      = "quota_status"
	ToolAgentsMDGet      = "agents_md_get"
	ToolAgentsMDValidate = "agents_md_validate"
	ToolAgentsMDEvaluate = "agents_md_evaluate"
)

// AllToolNames returns the canonical sorted list of registered tool
// names. The integration test asserts the registry contains exactly
// these.
func AllToolNames() []string {
	return []string{
		ToolAgentsMDEvaluate,
		ToolAgentsMDGet,
		ToolAgentsMDValidate,
		ToolCITrigger,
		ToolGitClone,
		ToolPRCreate,
		ToolQuotaStatus,
	}
}

// RegisterDefaults installs all seven canonical tools onto r against
// the supplied Deps. Panics on duplicate registration (programmer
// error). The Deps fields each tool needs are validated at call time
// — a nil dep that the tool requires causes the tool to return
// CodeNotImplemented at runtime, not at registration.
func RegisterDefaults(r *Registry, d Deps) {
	r.MustRegister(Tool{
		Name:        ToolGitClone,
		Description: "Mint a short-lived clone token + return clone URL for a repository the principal can access.",
		InputSchema: json.RawMessage(`{"type":"object","required":["repo_id"],"properties":{"repo_id":{"type":"string","format":"uuid"}}}`),
		Handler:     gitCloneHandler(d),
	})
	r.MustRegister(Tool{
		Name:        ToolPRCreate,
		Description: "Create a pull request. NOTE: this tool returns -32004 not_implemented until repositories.Service.CreatePullRequest ships in a follow-up issue.",
		InputSchema: json.RawMessage(`{"type":"object","required":["repo_id","title","source_ref","target_ref"],"properties":{"repo_id":{"type":"string","format":"uuid"},"title":{"type":"string"},"body":{"type":"string"},"source_ref":{"type":"string"},"target_ref":{"type":"string"}}}`),
		Handler:     prCreateHandler(d),
	})
	r.MustRegister(Tool{
		Name:        ToolCITrigger,
		Description: "Enqueue a CI run for a repository at a given ref via the workflow plane.",
		InputSchema: json.RawMessage(`{"type":"object","required":["repo_id","ref"],"properties":{"repo_id":{"type":"string","format":"uuid"},"ref":{"type":"string"}}}`),
		Handler:     ciTriggerHandler(d),
	})
	r.MustRegister(Tool{
		Name:        ToolQuotaStatus,
		Description: "Return the calling principal's MCP rate-limit bucket state (capacity, remaining, refill_per_sec, surface).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler:     quotaStatusHandler(d),
	})
	r.MustRegister(Tool{
		Name:        ToolAgentsMDGet,
		Description: "Return the merged (org + repo) AGENTS.md policy for a repository.",
		InputSchema: json.RawMessage(`{"type":"object","required":["repo_id"],"properties":{"repo_id":{"type":"string","format":"uuid"}}}`),
		Handler:     agentsMDGetHandler(d),
	})
	r.MustRegister(Tool{
		Name:        ToolAgentsMDValidate,
		Description: "Lint AGENTS.md content. Returns structured diagnostics (code, severity, line, message).",
		InputSchema: json.RawMessage(`{"type":"object","required":["content"],"properties":{"content":{"type":"string"}}}`),
		Handler:     agentsMDValidateHandler(),
	})
	r.MustRegister(Tool{
		Name:        ToolAgentsMDEvaluate,
		Description: "Dry-run the Never evaluator over caller-supplied ref updates and changed paths.",
		InputSchema: json.RawMessage(`{"type":"object","required":["repo_id","updates"],"properties":{"repo_id":{"type":"string","format":"uuid"},"updates":{"type":"array","items":{"type":"object","required":["ref_name","old_oid","new_oid"],"properties":{"ref_name":{"type":"string"},"old_oid":{"type":"string"},"new_oid":{"type":"string"}}}},"changed_paths":{"type":"array","items":{"type":"string"}},"file_sizes":{"type":"object","additionalProperties":{"type":"integer"}},"agents_md_content":{"type":"string"}}}`),
		Handler:     agentsMDEvaluateHandler(d),
	})
}

// ─── git_clone ───────────────────────────────────────────────────────────────

type gitCloneParams struct {
	RepoID string `json:"repo_id"`
}

// GitCloneResult is the JSON shape returned to the agent.
type GitCloneResult struct {
	CloneURL  string `json:"clone_url"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func gitCloneHandler(d Deps) ToolHandler {
	return func(ctx context.Context, p restapi.Principal, raw json.RawMessage) (any, error) {
		if d.Identity == nil || d.Repositories == nil {
			return nil, ErrNotImplemented
		}
		var in gitCloneParams
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", repositories.ErrInvalidSlug, err)
		}
		repoID, err := uuid.Parse(in.RepoID)
		if err != nil {
			return nil, fmt.Errorf("%w: repo_id is not a UUID", repositories.ErrInvalidSlug)
		}
		repo, err := d.Repositories.GetRepository(ctx, repoID)
		if err != nil {
			return nil, err
		}
		if repo == nil {
			return nil, repositories.ErrRepositoryNotFound
		}
		// Future-work: cross-org access check. The single-org default
		// today gives the principal access to every repo their identity
		// resolved against; the access matrix lands when org-membership
		// surfacing ships (#15-revocation downstream issue).
		ct, err := d.Identity.MintCloneToken(ctx, p.ID(), repoID)
		if err != nil {
			return nil, err
		}
		var url string
		if d.CloneURLBuilder != nil {
			url, err = d.CloneURLBuilder.BuildCloneURL(ctx, repo, ct.Token)
			if err != nil {
				return nil, err
			}
		} else {
			// Default URL shape: deployments without a builder get a
			// placeholder that includes the repo ID. Operators are
			// expected to wire a builder in cmd/mcp-server.
			url = fmt.Sprintf("https://gitscale.local/repos/%s.git", repo.ID)
		}
		return GitCloneResult{
			CloneURL:  url,
			Token:     ct.Token,
			ExpiresAt: ct.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}, nil
	}
}

// ─── pr_create ───────────────────────────────────────────────────────────────

func prCreateHandler(_ Deps) ToolHandler {
	// repositories.Service does not yet expose CreatePullRequest. Until
	// it ships (tracked as a follow-up issue), this tool returns
	// CodeNotImplemented. The description in tools/list mirrors that.
	return func(_ context.Context, _ restapi.Principal, _ json.RawMessage) (any, error) {
		return nil, ErrNotImplemented
	}
}

// ─── ci_trigger ──────────────────────────────────────────────────────────────

type ciTriggerParams struct {
	RepoID string `json:"repo_id"`
	Ref    string `json:"ref"`
}

// CITriggerResult is the JSON shape returned to the agent.
type CITriggerResult struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id"`
}

func ciTriggerHandler(d Deps) ToolHandler {
	return func(ctx context.Context, p restapi.Principal, raw json.RawMessage) (any, error) {
		if d.CIRunClient == nil {
			return nil, ErrNotImplemented
		}
		var in ciTriggerParams
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", repositories.ErrInvalidSlug, err)
		}
		repoID, err := uuid.Parse(in.RepoID)
		if err != nil {
			return nil, fmt.Errorf("%w: repo_id is not a UUID", repositories.ErrInvalidSlug)
		}
		if in.Ref == "" {
			return nil, fmt.Errorf("%w: ref is required", repositories.ErrInvalidSlug)
		}
		h, err := d.CIRunClient.StartCIRun(ctx, cirunclient.CIRunInput{
			RepoID: repoID, Ref: in.Ref, PrincipalID: p.ID(),
		})
		if err != nil {
			if errors.Is(err, cirunclient.ErrNotConfigured) {
				return nil, ErrNotImplemented
			}
			return nil, err
		}
		return CITriggerResult{WorkflowID: h.WorkflowID, RunID: h.RunID}, nil
	}
}

// ─── quota_status ────────────────────────────────────────────────────────────

// QuotaStatusResult is the JSON shape returned to the agent.
type QuotaStatusResult struct {
	Capacity     float64 `json:"capacity"`
	Remaining    float64 `json:"remaining"`
	RefillPerSec float64 `json:"refill_per_sec"`
	Surface      string  `json:"surface"`
}

func quotaStatusHandler(d Deps) ToolHandler {
	return func(ctx context.Context, p restapi.Principal, _ json.RawMessage) (any, error) {
		if d.Limiter == nil {
			return nil, ErrNotImplemented
		}
		key := fmt.Sprintf(ratelimit.TokenBucketKey, p.ID(), ratelimit.SurfaceMCP)
		state, err := d.Limiter.Inspect(ctx, key)
		if err != nil {
			return nil, err
		}
		return QuotaStatusResult{
			Capacity:     state.Capacity,
			Remaining:    state.Remaining,
			RefillPerSec: state.RefillPerSec,
			Surface:      ratelimit.SurfaceMCP,
		}, nil
	}
}

// ─── agents_md_validate ──────────────────────────────────────────────────────

type agentsMDValidateParams struct {
	Content string `json:"content"`
}

// AgentsMDDiagnostic mirrors agentsmd.Diagnostic as a JSON shape.
type AgentsMDDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// AgentsMDValidateResult is the JSON shape returned to the agent.
type AgentsMDValidateResult struct {
	Diagnostics []AgentsMDDiagnostic `json:"diagnostics"`
}

func agentsMDValidateHandler() ToolHandler {
	return func(_ context.Context, _ restapi.Principal, raw json.RawMessage) (any, error) {
		var in agentsMDValidateParams
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", repositories.ErrInvalidSlug, err)
		}
		diags := agentsmd.Lint([]byte(in.Content))
		out := AgentsMDValidateResult{Diagnostics: make([]AgentsMDDiagnostic, len(diags))}
		for i, d := range diags {
			out.Diagnostics[i] = AgentsMDDiagnostic{
				Code: d.Code, Severity: string(d.Severity), Line: d.Line, Message: d.Message,
			}
		}
		return out, nil
	}
}

// ─── agents_md_evaluate ──────────────────────────────────────────────────────

type agentsMDEvaluateParams struct {
	RepoID          string                  `json:"repo_id"`
	Updates         []agentsMDRefUpdate     `json:"updates"`
	ChangedPaths    []string                `json:"changed_paths"`
	FileSizes       map[string]int64        `json:"file_sizes"`
	AgentsMDContent string                  `json:"agents_md_content"` // optional override; otherwise resolved via deps
	IsFastForward   map[string]bool         `json:"is_fast_forward"`   // optional per-(old|new) override
}

type agentsMDRefUpdate struct {
	RefName string `json:"ref_name"`
	OldOID  string `json:"old_oid"`
	NewOID  string `json:"new_oid"`
}

// AgentsMDViolation mirrors agentsmd.Violation as a JSON shape.
type AgentsMDViolation struct {
	Predicate string `json:"predicate"`
	RefName   string `json:"ref_name"`
	Reason    string `json:"reason"`
}

// AgentsMDEvaluateResult is the JSON shape returned to the agent.
type AgentsMDEvaluateResult struct {
	Violations []AgentsMDViolation `json:"violations"`
}

func agentsMDEvaluateHandler(d Deps) ToolHandler {
	return func(ctx context.Context, _ restapi.Principal, raw json.RawMessage) (any, error) {
		var in agentsMDEvaluateParams
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", repositories.ErrInvalidSlug, err)
		}
		var content []byte
		if in.AgentsMDContent != "" {
			content = []byte(in.AgentsMDContent)
		} else if d.BlobReader != nil && in.RepoID != "" {
			data, err := d.BlobReader.ReadBlob(ctx, in.RepoID, "HEAD", "AGENTS.md")
			if err == nil {
				content = data
			}
			// Missing AGENTS.md is intentionally non-fatal — empty
			// policy ⇒ no violations.
		}
		policy, _, _ := agentsmd.Parse(content)
		updates := make([]agentsmd.RefUpdate, len(in.Updates))
		for i, u := range in.Updates {
			updates[i] = agentsmd.RefUpdate{RefName: u.RefName, OldOID: u.OldOID, NewOID: u.NewOID}
		}
		resolver := &inMemoryFileResolver{
			changed:   in.ChangedPaths,
			sizes:     in.FileSizes,
			fastFwd:   in.IsFastForward,
			fastFwdOK: true,
		}
		violations, err := agentsmd.Evaluate(ctx, policy, agentsmd.EvaluationInput{
			RepoID: in.RepoID, Updates: updates, Files: resolver,
		})
		if err != nil {
			return nil, err
		}
		out := AgentsMDEvaluateResult{Violations: make([]AgentsMDViolation, len(violations))}
		for i, v := range violations {
			out.Violations[i] = AgentsMDViolation{
				Predicate: string(v.Predicate.Name), RefName: v.RefName, Reason: v.Reason,
			}
		}
		return out, nil
	}
}

// inMemoryFileResolver implements agentsmd.FileResolver from
// caller-supplied maps. Production push enforcement uses the Gitaly-
// backed resolver from #114; this surface is for hypothetical /
// dry-run evaluation only.
type inMemoryFileResolver struct {
	changed   []string
	sizes     map[string]int64
	fastFwd   map[string]bool
	fastFwdOK bool
}

func (r *inMemoryFileResolver) Changed(_ context.Context, _, _ string) ([]string, error) {
	return r.changed, nil
}

func (r *inMemoryFileResolver) Size(_ context.Context, _, p string) (int64, error) {
	return r.sizes[p], nil
}

func (r *inMemoryFileResolver) IsFastForward(_ context.Context, oldOID, newOID string) (bool, error) {
	if v, ok := r.fastFwd[oldOID+":"+newOID]; ok {
		return v, nil
	}
	return r.fastFwdOK, nil
}

// ─── agents_md_get ───────────────────────────────────────────────────────────

type agentsMDGetParams struct {
	RepoID string `json:"repo_id"`
}

// AgentsMDPredicate mirrors agentsmd.NeverPredicate as a JSON shape.
type AgentsMDPredicate struct {
	Name     string `json:"name"`
	Selector struct {
		BranchGlob string `json:"branch_glob,omitempty"`
		PathGlob   string `json:"path_glob,omitempty"`
		MaxBytes   int64  `json:"max_bytes,omitempty"`
	} `json:"selector"`
}

// AgentsMDGetResult is the JSON shape returned to the agent.
type AgentsMDGetResult struct {
	SchemaVersion string              `json:"schema_version"`
	Empty         bool                `json:"empty"`
	Never         []AgentsMDPredicate `json:"never"`
}

func agentsMDGetHandler(d Deps) ToolHandler {
	return func(ctx context.Context, _ restapi.Principal, raw json.RawMessage) (any, error) {
		if d.Repositories == nil || d.BlobReader == nil {
			return nil, ErrNotImplemented
		}
		var in agentsMDGetParams
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", repositories.ErrInvalidSlug, err)
		}
		repoID, err := uuid.Parse(in.RepoID)
		if err != nil {
			return nil, fmt.Errorf("%w: repo_id is not a UUID", repositories.ErrInvalidSlug)
		}
		repo, err := d.Repositories.GetRepository(ctx, repoID)
		if err != nil {
			return nil, err
		}
		if repo == nil {
			return nil, repositories.ErrRepositoryNotFound
		}
		// Org policy via policystore (uses the same BlobReader).
		var orgBytes []byte
		if d.OrgPolicy != nil {
			ps := policystore.New(d.OrgPolicy, d.BlobReader)
			orgBytes, err = ps.ResolveOrgPolicyBytes(ctx, repoID.String())
			if err != nil {
				return nil, err
			}
		}
		// Repo AGENTS.md.
		repoBytes, err := d.BlobReader.ReadBlob(ctx, repoID.String(), "HEAD", "AGENTS.md")
		if err != nil && !isBlobNotFound(err) {
			return nil, err
		}
		orgPolicy, _, _ := agentsmd.Parse(orgBytes)
		repoPolicy, _, _ := agentsmd.Parse(repoBytes)
		merged := agentsmd.Merge(orgPolicy, repoPolicy)
		out := AgentsMDGetResult{
			SchemaVersion: merged.SchemaVersion,
			Empty:         merged.Empty,
			Never:         make([]AgentsMDPredicate, 0, len(merged.Never)),
		}
		for _, np := range merged.Never {
			pred := AgentsMDPredicate{Name: string(np.Name)}
			pred.Selector.BranchGlob = np.Selector.BranchGlob
			pred.Selector.PathGlob = np.Selector.PathGlob
			pred.Selector.MaxBytes = np.Selector.MaxBytes
			out.Never = append(out.Never, pred)
		}
		return out, nil
	}
}

// isBlobNotFound flags the BlobReader's "missing" sentinel.
func isBlobNotFound(err error) bool {
	return errors.Is(err, hook.ErrBlobNotFound)
}
