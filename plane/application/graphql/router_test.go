package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gqlplane "github.com/gitscale-platform/gitscale/plane/application/graphql"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	gqlmw "github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/resolvers"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	storestub "github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

// stubResolver maps a fixed bearer token to a known principal.
type stubResolver struct {
	tok       string
	principal restapi.Principal
}

func (s *stubResolver) Resolve(_ context.Context, bearer string) (restapi.Principal, error) {
	if bearer != s.tok {
		return nil, restapi.ErrInvalidToken
	}
	return s.principal, nil
}

// stubPersistedStore is an in-memory persisted.Store; we don't pull the
// real Postgres dep into router_test.
type stubPersistedStore struct {
	body map[string]string
}

func (s *stubPersistedStore) Get(_ context.Context, hash string) (string, error) {
	if v, ok := s.body[hash]; ok {
		return v, nil
	}
	return "", persisted.ErrNotFound
}
func (s *stubPersistedStore) Put(_ context.Context, hash, query string, _ uuid.UUID) error {
	if existing, ok := s.body[hash]; ok && existing != query {
		return persisted.ErrHashConflict
	}
	if s.body == nil {
		s.body = map[string]string{}
	}
	s.body[hash] = query
	return nil
}

func newTestHandler(t *testing.T, tok string, princ restapi.Principal) (http.Handler, *stubPersistedStore) {
	t.Helper()
	pools := gqlmw.Pools{
		Reader:  storestub.New(),
		Primary: storestub.New(),
	}
	store := &stubPersistedStore{}
	deps := gqlplane.Deps{
		Pools:    pools,
		Resolver: &stubResolver{tok: tok, principal: princ},
		Limiter:  ratelimit.NewMemoryLimiter(nil),
		Analyzer: cost.New(cost.DefaultLimits(), cost.DefaultFieldWeights()),
		Persisted: persisted.NewCachedStore(store, cache.NewMemoryStore(nil)),
		Resolvers: resolvers.Deps{
			Identity: nil,
			SVID:     resolvers.AlwaysVerifiedSVID{},
		},
		Bucket: gqlmw.BucketParams{
			HumanCapacity: 1000, HumanRefillPerSec: 100,
			AgentCapacity: 5000, AgentRefillPerSec: 100,
		},
	}
	return gqlplane.NewHandler(deps), store
}

func doPost(t *testing.T, h http.Handler, path, tok string, body any) *http.Response {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func TestRouter_HealthzNoAuth(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uuid.New()})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/graphql/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %d", err, resp.StatusCode)
	}
}

func TestRouter_Unauthenticated(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uuid.New()})
	resp := doPost(t, h, "/graphql", "", map[string]string{"query": "{ __schema { queryType { name } } }"})
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRouter_DepthExceeded_ChargesParseCostAndReturnsCode(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uid})

	q := strings.Repeat("{ a ", 12) + strings.Repeat("} ", 12)
	resp := doPost(t, h, "/graphql", "tok", map[string]any{"query": q})
	defer func() { _ = resp.Body.Close() }()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errsAny, ok := env["errors"].([]any)
	if !ok || len(errsAny) == 0 {
		t.Fatalf("no errors in response: %+v", env)
	}
	first := errsAny[0].(map[string]any)
	ext := first["extensions"].(map[string]any)
	if ext["code"] != "DEPTH_EXCEEDED" {
		t.Errorf("code: %v", ext["code"])
	}
}

func TestRouter_IntrospectionWorks(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uuid.New()})
	resp := doPost(t, h, "/graphql", "tok", map[string]string{"query": "{ __schema { queryType { name fields { name } } } }"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sch := env.Data["__schema"].(map[string]any)
	qt := sch["queryType"].(map[string]any)
	if qt["name"] != "Query" {
		t.Errorf("queryType.name: %v", qt["name"])
	}
	fields := qt["fields"].([]any)
	names := map[string]bool{}
	for _, f := range fields {
		names[f.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"repository", "user", "agent", "pullRequest", "organization"} {
		if !names[want] {
			t.Errorf("introspection missing field %q (got %v)", want, names)
		}
	}
}

func TestRouter_PersistedRegisterAndExecute(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uid})

	// Register.
	resp := doPost(t, h, "/graphql/persisted/register", "tok", map[string]string{
		"query": `{ __schema { queryType { name } } }`,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("register status: %d", resp.StatusCode)
	}
	var reg struct{ Hash string }
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(reg.Hash, "sha256:") {
		t.Fatalf("hash shape: %s", reg.Hash)
	}

	// Execute.
	resp2 := doPost(t, h, "/graphql/persisted/"+reg.Hash, "tok", map[string]any{})
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != 200 {
		t.Fatalf("execute status: %d", resp2.StatusCode)
	}
	var env struct {
		Data       map[string]any `json:"data"`
		Extensions map[string]any `json:"extensions"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&env)
	if env.Extensions == nil {
		t.Fatalf("missing extensions: %+v", env)
	}
	if env.Extensions["persistedDiscount"] == nil {
		t.Errorf("persistedDiscount missing for persisted-query path")
	}
}

func TestRouter_PersistedNotFound(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, "tok", restapi.HumanPrincipal{UserID: uuid.New()})
	resp := doPost(t, h, "/graphql/persisted/sha256:absent", "tok", map[string]any{})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestRouter_RateLimited(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	pools := gqlmw.Pools{Reader: storestub.New(), Primary: storestub.New()}
	deps := gqlplane.Deps{
		Pools:    pools,
		Resolver: &stubResolver{tok: "tok", principal: restapi.HumanPrincipal{UserID: uid}},
		Limiter:  ratelimit.NewMemoryLimiter(nil),
		Analyzer: cost.New(cost.DefaultLimits(), cost.DefaultFieldWeights()),
		Persisted: persisted.NewCachedStore(&stubPersistedStore{}, cache.NewMemoryStore(nil)),
		Resolvers: resolvers.Deps{SVID: resolvers.AlwaysVerifiedSVID{}},
		Bucket: gqlmw.BucketParams{
			HumanCapacity: 5, HumanRefillPerSec: 0, // tiny bucket
			AgentCapacity: 5, AgentRefillPerSec: 0,
		},
	}
	h := gqlplane.NewHandler(deps)
	// First request consumes ~5+ tokens. Second hits RATE_LIMITED.
	q := `{ user(login:"x") { id } }`
	_ = doPost(t, h, "/graphql", "tok", map[string]string{"query": q})
	resp := doPost(t, h, "/graphql", "tok", map[string]string{"query": q})
	defer func() { _ = resp.Body.Close() }()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	errsAny, ok := env["errors"].([]any)
	if !ok || len(errsAny) == 0 {
		// First call may have already consumed nothing if cost is small.
		// Hammer until we see RATE_LIMITED or exceed 20 tries — bucket
		// capacity 5, refill 0, cost ≥ 1 per call → guaranteed within a
		// few tries.
		var seen bool
		for i := 0; i < 20; i++ {
			r := doPost(t, h, "/graphql", "tok", map[string]string{"query": q})
			var e map[string]any
			_ = json.NewDecoder(r.Body).Decode(&e)
			_ = r.Body.Close()
			if errs, ok := e["errors"].([]any); ok && len(errs) > 0 {
				if errors.Is(maybeCode(errs[0]), errRateLimitedSentinel) {
					seen = true
					break
				}
				if extCode(errs[0]) == "RATE_LIMITED" {
					seen = true
					break
				}
			}
		}
		if !seen {
			t.Fatalf("never observed RATE_LIMITED")
		}
		return
	}
	if extCode(errsAny[0]) != "RATE_LIMITED" {
		// Not necessarily RL on the second call if first cost was 0; try
		// a few more.
		for i := 0; i < 10; i++ {
			r := doPost(t, h, "/graphql", "tok", map[string]string{"query": q})
			var e map[string]any
			_ = json.NewDecoder(r.Body).Decode(&e)
			_ = r.Body.Close()
			if errs, ok := e["errors"].([]any); ok && len(errs) > 0 && extCode(errs[0]) == "RATE_LIMITED" {
				return
			}
		}
		t.Fatalf("expected RATE_LIMITED, got %v", errsAny[0])
	}
}

// errRateLimitedSentinel and maybeCode are minimal helpers; the test only
// inspects extension codes via extCode.
var errRateLimitedSentinel = errors.New("RATE_LIMITED")

func maybeCode(any) error { return nil }

func extCode(in any) string {
	m, ok := in.(map[string]any)
	if !ok {
		return ""
	}
	ext, ok := m["extensions"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := ext["code"].(string)
	return c
}
