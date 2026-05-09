package policystore

import (
	"context"
	"errors"
	"testing"

	hookpkg "github.com/gitscale-platform/gitscale/plane/application/agentsmd/hook"
)

type fakeMeta struct {
	repoToOrg     map[string]string
	repoToSlug    map[string]string
	slugToRepoID  map[string]string // "org/slug" -> repoID
	lookupErr     error
	bySlugErr     error
}

func (f *fakeMeta) LookupRepoSlug(_ context.Context, repoID string) (string, string, error) {
	if f.lookupErr != nil {
		return "", "", f.lookupErr
	}
	org, ok := f.repoToOrg[repoID]
	if !ok {
		return "", "", ErrNotFound
	}
	return org, f.repoToSlug[repoID], nil
}
func (f *fakeMeta) LookupRepoIDBySlug(_ context.Context, org, slug string) (string, error) {
	if f.bySlugErr != nil {
		return "", f.bySlugErr
	}
	id, ok := f.slugToRepoID[org+"/"+slug]
	if !ok {
		return "", ErrNotFound
	}
	return id, nil
}

type fakeBlobs struct {
	blobs map[string][]byte
	err   error
}

func (b *fakeBlobs) ReadBlob(_ context.Context, repoID, ref, path string) ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	if data, ok := b.blobs[repoID+"|"+ref+"|"+path]; ok {
		return data, nil
	}
	return nil, hookpkg.ErrBlobNotFound
}
func (b *fakeBlobs) ListChangedPaths(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}
func (b *fakeBlobs) BlobSize(_ context.Context, _, _, _ string) (int64, error) { return 0, nil }
func (b *fakeBlobs) IsFastForward(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

func TestResolve_OrgUnknown(t *testing.T) {
	s := New(&fakeMeta{}, &fakeBlobs{})
	got, err := s.ResolveOrgPolicyBytes(context.Background(), "missing")
	if err != nil || got != nil {
		t.Fatalf("expected (nil,nil), got %v %v", got, err)
	}
}

func TestResolve_OrgKnownButNoPolicyRepo(t *testing.T) {
	meta := &fakeMeta{
		repoToOrg:    map[string]string{"r1": "acme"},
		repoToSlug:   map[string]string{"r1": "service"},
		slugToRepoID: map[string]string{},
	}
	s := New(meta, &fakeBlobs{})
	got, err := s.ResolveOrgPolicyBytes(context.Background(), "r1")
	if err != nil || got != nil {
		t.Fatalf("expected (nil,nil), got %v %v", got, err)
	}
}

func TestResolve_PolicyRepoExistsButNoAgentsMd(t *testing.T) {
	meta := &fakeMeta{
		repoToOrg:    map[string]string{"r1": "acme"},
		repoToSlug:   map[string]string{"r1": "service"},
		slugToRepoID: map[string]string{"acme/" + AgentsPolicyRepoSlug: "policy-repo"},
	}
	s := New(meta, &fakeBlobs{blobs: map[string][]byte{}})
	got, err := s.ResolveOrgPolicyBytes(context.Background(), "r1")
	if err != nil || got != nil {
		t.Fatalf("expected (nil,nil), got %v %v", got, err)
	}
}

func TestResolve_BlobFound(t *testing.T) {
	doc := []byte("---\nschema: gitscale/v1\n---\n## Never\n- delete_branch: main\n")
	meta := &fakeMeta{
		repoToOrg:    map[string]string{"r1": "acme"},
		repoToSlug:   map[string]string{"r1": "service"},
		slugToRepoID: map[string]string{"acme/" + AgentsPolicyRepoSlug: "policy-repo"},
	}
	blobs := &fakeBlobs{blobs: map[string][]byte{"policy-repo|HEAD|AGENTS.md": doc}}
	s := New(meta, blobs)
	got, err := s.ResolveOrgPolicyBytes(context.Background(), "r1")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if string(got) != string(doc) {
		t.Fatalf("blob mismatch: %q", got)
	}
}

func TestResolve_MetaErrorPropagates(t *testing.T) {
	s := New(&fakeMeta{lookupErr: errors.New("postgres down")}, &fakeBlobs{})
	_, err := s.ResolveOrgPolicyBytes(context.Background(), "r1")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestResolve_BlobInfraErrorPropagates(t *testing.T) {
	meta := &fakeMeta{
		repoToOrg:    map[string]string{"r1": "acme"},
		repoToSlug:   map[string]string{"r1": "service"},
		slugToRepoID: map[string]string{"acme/" + AgentsPolicyRepoSlug: "policy-repo"},
	}
	s := New(meta, &fakeBlobs{err: errors.New("gitaly down")})
	_, err := s.ResolveOrgPolicyBytes(context.Background(), "r1")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNew_NilArgsPanic(t *testing.T) {
	for _, tc := range []func(){
		func() { New(nil, &fakeBlobs{}) },
		func() { New(&fakeMeta{}, nil) },
	} {
		func() {
			defer func() { _ = recover() }()
			tc()
			t.Fatalf("expected panic")
		}()
	}
}
