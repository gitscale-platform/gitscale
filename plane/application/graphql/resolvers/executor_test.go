package resolvers_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/resolvers"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// fakeMetadataStore is a tiny store.MetadataStore implementation that the
// executor tests use. The plane/data/store/stub package is also a valid
// choice but it lacks the seed surface we want here.
type fakeMetadataStore struct {
	users  map[uuid.UUID]*store.HumanUser
	emails map[string]*store.HumanUser
	agents map[uuid.UUID]*store.AgentIdentity
	repos  map[string]*store.Repository
}

func newFakeStore() *fakeMetadataStore {
	return &fakeMetadataStore{
		users:  map[uuid.UUID]*store.HumanUser{},
		emails: map[string]*store.HumanUser{},
		agents: map[uuid.UUID]*store.AgentIdentity{},
		repos:  map[string]*store.Repository{},
	}
}

func (f *fakeMetadataStore) Transact(_ context.Context, _ func(store.Tx) error) error {
	return nil
}

func (f *fakeMetadataStore) Identity() store.IdentityReader     { return &fakeIdentityReader{f} }
func (f *fakeMetadataStore) Repositories() store.RepositoryReader { return &fakeRepoReader{f} }
func (f *fakeMetadataStore) Billing() store.BillingReader         { return nil }

type fakeIdentityReader struct{ f *fakeMetadataStore }

func (r *fakeIdentityReader) GetUserByID(_ context.Context, id uuid.UUID) (*store.HumanUser, error) {
	if u, ok := r.f.users[id]; ok {
		return u, nil
	}
	return nil, nil
}
func (r *fakeIdentityReader) GetUserByEmail(_ context.Context, email string) (*store.HumanUser, error) {
	if u, ok := r.f.emails[email]; ok {
		return u, nil
	}
	return nil, nil
}
func (r *fakeIdentityReader) GetAgentByID(_ context.Context, id uuid.UUID) (*store.AgentIdentity, error) {
	if a, ok := r.f.agents[id]; ok {
		return a, nil
	}
	return nil, nil
}
func (r *fakeIdentityReader) GetAgentsByParentUser(_ context.Context, _ uuid.UUID) ([]store.AgentIdentity, error) {
	return nil, nil
}
func (r *fakeIdentityReader) LookupIdentityForCache(_ context.Context, _ uuid.UUID) (*store.IdentityCacheEntry, error) {
	return nil, nil
}

type fakeRepoReader struct{ f *fakeMetadataStore }

func (r *fakeRepoReader) GetByID(_ context.Context, id uuid.UUID) (*store.Repository, error) {
	for _, repo := range r.f.repos {
		if repo.ID == id {
			return repo, nil
		}
	}
	return nil, nil
}
func (r *fakeRepoReader) GetBySlug(_ context.Context, slug string) (*store.Repository, error) {
	if v, ok := r.f.repos[slug]; ok {
		return v, nil
	}
	return nil, nil
}
func (r *fakeRepoReader) ListByOrg(_ context.Context, _ uuid.UUID, _ *time.Time, _ *uuid.UUID, _ int) ([]store.Repository, error) {
	return nil, nil
}

// stubIdentityService implements identity.Service just enough to drive
// the createAgent mutation path.
type stubIdentityService struct{}

func (stubIdentityService) GetUser(context.Context, uuid.UUID) (*identity.HumanUser, error) {
	return nil, nil
}
func (stubIdentityService) GetUserByEmail(context.Context, string) (*identity.HumanUser, error) {
	return nil, nil
}
func (stubIdentityService) GetAgent(context.Context, uuid.UUID) (*identity.AgentIdentity, error) {
	return nil, nil
}
func (stubIdentityService) GetAgentsByParentUser(context.Context, uuid.UUID) ([]identity.AgentIdentity, error) {
	return nil, nil
}
func (stubIdentityService) LookupIdentityForCache(context.Context, uuid.UUID) (*cache.IdentityCacheEntry, error) {
	return nil, nil
}
func (stubIdentityService) CreateUser(context.Context, string, string) (*identity.HumanUser, error) {
	return nil, nil
}
func (stubIdentityService) CreateAgent(_ context.Context, parent uuid.UUID, displayName string, scope []string) (*identity.AgentIdentity, error) {
	return &identity.AgentIdentity{
		ID:              uuid.New(),
		DisplayName:     displayName,
		ParentUserID:    parent,
		PermissionScope: scope,
	}, nil
}
func (stubIdentityService) SetAgentReputationScore(context.Context, uuid.UUID, float64) error {
	return nil
}
func (stubIdentityService) DisableUser(context.Context, uuid.UUID, string) error { return nil }
func (stubIdentityService) RevokeAgent(context.Context, uuid.UUID, string) error { return nil }
func (stubIdentityService) UpdateAgentPermissions(context.Context, uuid.UUID, []string) error {
	return nil
}
func (stubIdentityService) AddOrgMember(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (stubIdentityService) RemoveOrgMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (stubIdentityService) MintCloneToken(context.Context, uuid.UUID, uuid.UUID) (identity.CloneToken, error) {
	return identity.CloneToken{}, nil
}

func mustExecute(t *testing.T, ctx context.Context, src string) resolvers.Result {
	t.Helper()
	doc, err := cost.ParseQuery(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	exec := &resolvers.Executor{Deps: resolvers.Deps{
		Identity: stubIdentityService{},
	}}
	return exec.Execute(ctx, doc, doc.Operations[0])
}

func TestExecutor_QueryUserByEmail(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	uid := uuid.New()
	u := storeUser(t, uid, "alice@example.com")
	st.users[uid] = &u
	st.emails[u.Email] = &u

	ctx := middleware.WithStore(context.Background(), st)
	res := mustExecute(t, ctx, `{ user(login: "alice@example.com") { id login email } }`)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	got, _ := json.Marshal(res.Data)
	if !strings.Contains(string(got), uid.String()) {
		t.Errorf("missing user id in result: %s", got)
	}
}

func TestExecutor_NotFoundEmitsError(t *testing.T) {
	t.Parallel()
	ctx := middleware.WithStore(context.Background(), newFakeStore())
	res := mustExecute(t, ctx, `{ user(login: "nope@example.com") { id } }`)
	if len(res.Errors) == 0 {
		t.Fatalf("expected NOT_FOUND error, got none")
	}
}

func TestExecutor_RepositoryShallow(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	rid := uuid.New()
	repo := storeRepo(t, rid, "demo", "Demo")
	st.repos["demo"] = &repo
	ctx := middleware.WithStore(context.Background(), st)
	res := mustExecute(t, ctx, `{ repository(owner: "acme", name: "demo") { id name visibility } }`)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	got := res.Data["repository"].(map[string]any)
	if got["id"] != rid.String() {
		t.Errorf("id: %v", got["id"])
	}
	if got["name"] != "Demo" {
		t.Errorf("name: %v", got["name"])
	}
}

func TestExecutor_IntrospectionWorks(t *testing.T) {
	t.Parallel()
	ctx := middleware.WithStore(context.Background(), newFakeStore())
	res := mustExecute(t, ctx, `{ __schema { queryType { name fields { name } } } }`)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	sch := res.Data["__schema"].(map[string]any)
	qt := sch["queryType"].(map[string]any)
	if qt["name"] != "Query" {
		t.Errorf("queryType.name: %v", qt["name"])
	}
}

func TestExecutor_MutationCreateAgentRequiresHuman(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	ctx := middleware.WithPrincipal(
		middleware.WithStore(context.Background(), newFakeStore()),
		middleware.Principal{Kind: middleware.PrincipalAgent, ID: uid, ParentUserID: uid},
	)
	res := mustExecute(t, ctx, `mutation { createAgent(input: {parentUserId: "`+uid.String()+`", displayName: "x", permissionScope: ["repo:read"]}) { agent { id } } }`)
	if len(res.Errors) == 0 {
		t.Fatalf("agent calling createAgent must be FORBIDDEN")
	}
}

func TestExecutor_MutationCreateAgentHumanSucceeds(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	ctx := middleware.WithPrincipal(
		middleware.WithStore(context.Background(), newFakeStore()),
		middleware.Principal{Kind: middleware.PrincipalHuman, ID: uid},
	)
	res := mustExecute(t, ctx, `mutation { createAgent(input: {parentUserId: "`+uid.String()+`", displayName: "x", permissionScope: ["repo:read"]}) { agent { id displayName } } }`)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if res.Data["createAgent"] == nil {
		t.Errorf("missing createAgent payload")
	}
}

// storeUser / storeRepo are pointer-deref helpers.
func storeUser(t *testing.T, id uuid.UUID, email string) store.HumanUser {
	t.Helper()
	return store.HumanUser{ID: id, Email: email, CreatedAt: time.Now()}
}

func storeRepo(t *testing.T, id uuid.UUID, slug, name string) store.Repository {
	t.Helper()
	return store.Repository{
		ID: id, Slug: slug, Name: name, OrgID: uuid.New(),
		OwnerID: uuid.New(), Visibility: "private",
		DefaultBranch: "main", CreatedAt: time.Now(),
	}
}
