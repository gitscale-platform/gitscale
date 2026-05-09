package hook

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/agentsmd"
	"github.com/gitscale-platform/gitscale/plane/git/gittypes"
)

// memBlobs is an in-memory BlobReader for unit tests.
// Blobs map key: "<repoID>|<ref>|<path>". A missing key returns
// ErrBlobNotFound. listErr/sizeErr/ffErr override behaviour for the
// non-blob calls when set.
type memBlobs struct {
	blobs   map[string][]byte
	changed map[string][]string // "<repoID>|<old>..<new>"
	sizes   map[string]int64    // "<repoID>|<oid>:<path>"
	ff      map[string]bool     // "<repoID>|<old>..<new>"
	readErr error
}

func (m *memBlobs) ReadBlob(_ context.Context, repoID, ref, path string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	k := repoID + "|" + ref + "|" + path
	if b, ok := m.blobs[k]; ok {
		return b, nil
	}
	return nil, ErrBlobNotFound
}
func (m *memBlobs) ListChangedPaths(_ context.Context, repoID, oldOID, newOID string) ([]string, error) {
	return m.changed[repoID+"|"+oldOID+".."+newOID], nil
}
func (m *memBlobs) BlobSize(_ context.Context, repoID, oid, path string) (int64, error) {
	return m.sizes[repoID+"|"+oid+":"+path], nil
}
func (m *memBlobs) IsFastForward(_ context.Context, repoID, oldOID, newOID string) (bool, error) {
	return m.ff[repoID+"|"+oldOID+".."+newOID], nil
}

type stubStore struct {
	bytes []byte
	err   error
}

func (s *stubStore) ResolveOrgPolicyBytes(_ context.Context, _ string) ([]byte, error) {
	return s.bytes, s.err
}

func TestHandler_NewPanicsOnNil(t *testing.T) {
	defer func() { _ = recover() }()
	New(nil, &stubStore{})
	t.Fatalf("expected panic on nil BlobReader")
}

func TestHandler_NoPolicyAllowsPush(t *testing.T) {
	h := New(&memBlobs{blobs: map[string][]byte{}}, &stubStore{})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestHandler_RepoPolicyRejectsDelete(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	blobs := &memBlobs{blobs: map[string][]byte{
		"r1|old|AGENTS.md": doc,
	}}
	h := New(blobs, &stubStore{})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: agentsmd.ZeroOID},
	})
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if !strings.Contains(err.Error(), "AGENTS.md Never violation") {
		t.Fatalf("expected structured message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "delete_branch") {
		t.Fatalf("expected predicate name, got %q", err.Error())
	}
}

func TestHandler_OrgPolicyAppliesEvenWithoutRepoPolicy(t *testing.T) {
	orgDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	blobs := &memBlobs{blobs: map[string][]byte{}}
	h := New(blobs, &stubStore{bytes: orgDoc})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: agentsmd.ZeroOID},
	})
	if err == nil {
		t.Fatalf("expected rejection from org policy")
	}
}

func TestHandler_MalformedPolicyDoesNotBlockPush(t *testing.T) {
	// No front-matter delimiter -> parse error -> empty policy -> push allowed.
	bad := []byte("not a valid agents.md\n## Never\n- delete_branch: main\n")
	blobs := &memBlobs{blobs: map[string][]byte{
		"r1|new|AGENTS.md": bad,
	}}
	h := New(blobs, &stubStore{})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
	})
	if err != nil {
		t.Fatalf("malformed policy must not block push: %v", err)
	}
}

func TestHandler_BlobReaderInfraErrorRejectsPush(t *testing.T) {
	blobs := &memBlobs{readErr: errors.New("gitaly down")}
	h := New(blobs, &stubStore{})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
	})
	if err == nil {
		t.Fatalf("infra error must reject push")
	}
}

func TestHandler_PolicyStoreErrorRejectsPush(t *testing.T) {
	blobs := &memBlobs{blobs: map[string][]byte{}}
	h := New(blobs, &stubStore{err: errors.New("postgres down")})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: "new"},
	})
	if err == nil {
		t.Fatalf("policy-store error must reject push")
	}
}

func TestHandler_OrgWinsOnConflict(t *testing.T) {
	// Org rejects delete of main; repo would too. Single violation
	// expected (org predicate, repo dedup'd).
	orgDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	repoDoc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	blobs := &memBlobs{blobs: map[string][]byte{
		"r1|old|AGENTS.md": repoDoc,
	}}
	h := New(blobs, &stubStore{bytes: orgDoc})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: agentsmd.ZeroOID},
	})
	if err == nil || strings.Count(err.Error(), "Never violation") != 1 {
		t.Fatalf("expected exactly one violation, got %v", err)
	}
}

func TestHandler_MultipleViolationsJoined(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n- push_to_branch: \"release/*\"\n")
	blobs := &memBlobs{blobs: map[string][]byte{
		"r1|new1|AGENTS.md": doc,
		"r1|new2|AGENTS.md": doc,
	}}
	h := New(blobs, &stubStore{})
	err := h.PreReceive(context.Background(), gittypes.RepoRef{RepoID: "r1"}, []gittypes.RefUpdate{
		{RefName: "refs/heads/main", OldOID: "old", NewOID: agentsmd.ZeroOID},
		{RefName: "refs/heads/release/v1", OldOID: "a", NewOID: "new2"},
	})
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if got := strings.Count(err.Error(), "Never violation"); got != 2 {
		t.Fatalf("expected 2 violations joined, got %d in %q", got, err.Error())
	}
}
