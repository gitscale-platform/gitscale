package agentsmd_test

// Integration-style coverage for the AGENTS.md surfacing + Never
// enforcement chain: parser → merge → evaluator → hook adapter →
// policystore. We deliberately avoid testcontainers Gitaly+Postgres
// here because the git-rpc binary that would drive a real push is not
// yet built (#107 ships only the proxy package; the cmd/* entrypoint
// lands separately). Once that binary exists, the testcontainers-based
// version of these scenarios should live alongside it. The unit-level
// tests in handler_test.go and policystore_test.go cover every branch
// of error handling.

import (
	"context"
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/agentsmd"
	hookpkg "github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
	"github.com/gitscale-platform/gitscale/plane/application/agentsmd/policystore"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

type harnessBlobs struct {
	blobs map[string][]byte
	ff    map[string]bool
}

func (h *harnessBlobs) ReadBlob(_ context.Context, repoID, ref, path string) ([]byte, error) {
	if data, ok := h.blobs[repoID+"|"+ref+"|"+path]; ok {
		return data, nil
	}
	return nil, hookpkg.ErrBlobNotFound
}
func (h *harnessBlobs) ListChangedPaths(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}
func (h *harnessBlobs) BlobSize(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}
func (h *harnessBlobs) IsFastForward(_ context.Context, _, oldOID, newOID string) (bool, error) {
	return h.ff[oldOID+".."+newOID], nil
}

type harnessMeta struct {
	org  string
	slug string
}

func (h *harnessMeta) LookupRepoSlug(_ context.Context, _ string) (string, string, error) {
	if h.org == "" {
		return "", "", policystore.ErrNotFound
	}
	return h.org, "service", nil
}
func (h *harnessMeta) LookupRepoIDBySlug(_ context.Context, org, slug string) (string, error) {
	if h.slug == "" || org != h.org || slug != policystore.AgentsPolicyRepoSlug {
		return "", policystore.ErrNotFound
	}
	return h.slug, nil
}

func newHarness(t *testing.T, repoDoc, orgDoc []byte) *hookpkg.Handler {
	t.Helper()
	blobs := &harnessBlobs{blobs: map[string][]byte{}}
	if repoDoc != nil {
		blobs.blobs["service-repo|tip|AGENTS.md"] = repoDoc
		blobs.blobs["service-repo|HEAD|AGENTS.md"] = repoDoc
	}
	meta := &harnessMeta{}
	if orgDoc != nil {
		meta.org = "acme"
		meta.slug = "policy-repo"
		blobs.blobs["policy-repo|HEAD|AGENTS.md"] = orgDoc
	}
	store := policystore.New(meta, blobs)
	return hookpkg.New(blobs, store)
}

func TestIntegration_PushViolatingNeverIsRejected(t *testing.T) {
	repoDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	h := newHarness(t, repoDoc, nil)

	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "service-repo"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "tip", NewOID: agentsmd.ZeroOID},
	})
	if err == nil {
		t.Fatalf("expected push rejection")
	}
	if !strings.Contains(err.Error(), "AGENTS.md Never violation: delete_branch") {
		t.Fatalf("expected structured violation message, got %q", err.Error())
	}
}

func TestIntegration_CleanPushIsAccepted(t *testing.T) {
	repoDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	h := newHarness(t, repoDoc, nil)

	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "service-repo"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/feature/x", OldOID: "tip", NewOID: "newtip"},
	})
	if err != nil {
		t.Fatalf("clean push must pass, got %v", err)
	}
}

func TestIntegration_MalformedAgentsMdDoesNotBlock(t *testing.T) {
	bad := []byte("not valid agents.md\nno front matter\n")
	h := newHarness(t, bad, nil)

	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "service-repo"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "tip", NewOID: "newtip"},
	})
	if err != nil {
		t.Fatalf("malformed AGENTS.md must not block push (lint surface only): %v", err)
	}
}

func TestIntegration_OrgPolicyEnforcedWithoutRepoPolicy(t *testing.T) {
	orgDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	h := newHarness(t, nil, orgDoc)

	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "service-repo"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "tip", NewOID: agentsmd.ZeroOID},
	})
	if err == nil {
		t.Fatalf("expected org-policy rejection")
	}
}

func TestIntegration_ForcePushBlockedAcrossOrgRepoMerge(t *testing.T) {
	orgDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- force_push_to_branch: main\n")
	repoDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	blobs := &harnessBlobs{
		blobs: map[string][]byte{
			"service-repo|HEAD|AGENTS.md": repoDoc,
			"service-repo|new|AGENTS.md":  repoDoc,
			"policy-repo|HEAD|AGENTS.md":  orgDoc,
		},
		ff: map[string]bool{"old..new": false}, // not a fast-forward -> force push
	}
	meta := &harnessMeta{org: "acme", slug: "policy-repo"}
	store := policystore.New(meta, blobs)
	h := hookpkg.New(blobs, store)

	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "service-repo"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
	})
	if err == nil {
		t.Fatalf("expected force-push rejection")
	}
	if !strings.Contains(err.Error(), "force_push_to_branch") {
		t.Fatalf("expected force_push_to_branch in message, got %q", err.Error())
	}
}
