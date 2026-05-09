package policystore

import (
	"context"
	"errors"
	"fmt"

	hookpkg "github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
)

// AgentsPolicyRepoSlug is the conventional repo slug under which an
// organisation hosts its org-wide AGENTS.md policy.
const AgentsPolicyRepoSlug = ".gitscale-agents"

// RepoMetadata is the slice of the application-plane repository service
// this package consumes. Defining it locally avoids a hard dependency on
// the (not-yet-built) repositories service. The wiring layer is
// responsible for implementing this interface against whatever metadata
// surface ships.
//
// LookupRepoSlug maps a repository ID to its (org_slug, repo_slug) pair.
// LookupRepoIDBySlug is the inverse for the conventional .gitscale-agents
// lookup. Either method may return ErrNotFound to signal absence; any
// other error is treated as load-bearing infra failure by the caller.
type RepoMetadata interface {
	LookupRepoSlug(ctx context.Context, repoID string) (orgSlug string, repoSlug string, err error)
	LookupRepoIDBySlug(ctx context.Context, orgSlug, repoSlug string) (repoID string, err error)
}

// ErrNotFound is the sentinel returned by RepoMetadata implementations
// when a repository or org cannot be resolved. It is intentionally
// distinct from hook.ErrBlobNotFound so callers can keep "no policy"
// (allow-through) separate from "infra error" (reject).
var ErrNotFound = errors.New("policystore: not found")

// ServicePolicyStore is the production PolicyStore implementation. It
// resolves the requesting repo's org via RepoMetadata, derives the
// conventional ".gitscale-agents" repo for that org, and fetches the
// AGENTS.md blob via BlobReader.
//
// Construction: see New. The zero value is not usable.
type ServicePolicyStore struct {
	meta  RepoMetadata
	blobs hookpkg.BlobReader
}

// Compile-time check.
var _ hookpkg.PolicyStore = (*ServicePolicyStore)(nil)

// New constructs a ServicePolicyStore. Both arguments must be non-nil.
func New(meta RepoMetadata, blobs hookpkg.BlobReader) *ServicePolicyStore {
	if meta == nil {
		panic("policystore: nil RepoMetadata")
	}
	if blobs == nil {
		panic("policystore: nil BlobReader")
	}
	return &ServicePolicyStore{meta: meta, blobs: blobs}
}

// ResolveOrgPolicyBytes returns the bytes of the org-wide AGENTS.md, or
// (nil, nil) if the source repo's org is unknown, the org has no
// .gitscale-agents repo, or that repo has no AGENTS.md.
//
// Errors are returned only for infra failures (RepoMetadata or
// BlobReader returning a non-sentinel error). The hook.Handler treats
// such errors as load-bearing and rejects the push.
func (s *ServicePolicyStore) ResolveOrgPolicyBytes(ctx context.Context, repoID string) ([]byte, error) {
	orgSlug, _, err := s.meta.LookupRepoSlug(ctx, repoID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("policystore: lookup repo %s: %w", repoID, err)
	}
	policyRepoID, err := s.meta.LookupRepoIDBySlug(ctx, orgSlug, AgentsPolicyRepoSlug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("policystore: lookup %s/%s: %w", orgSlug, AgentsPolicyRepoSlug, err)
	}
	data, err := s.blobs.ReadBlob(ctx, policyRepoID, "HEAD", "AGENTS.md")
	if err != nil {
		if errors.Is(err, hookpkg.ErrBlobNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("policystore: read org AGENTS.md: %w", err)
	}
	return data, nil
}
