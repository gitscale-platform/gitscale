//go:build integration

package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gqlplane "github.com/gitscale-platform/gitscale/plane/application/graphql"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	gqlmw "github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/resolvers"
	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/cache"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("gitscale_test"),
		pgmodule.WithUsername("gs"),
		pgmodule.WithPassword("gs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	connStr, _ := ctr.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	migrationsDir := filepath.Join("..", "..", "..", "plane", "data", "migrations")
	for _, f := range []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		"006_identity_revocation.sql",
		"010_repositories_keyset_index.sql",
		"011_graphql_persisted_queries.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
	return pool
}

func TestGraphQL_PostgresEndToEnd(t *testing.T) {
	pool := setupPG(t)
	mds := storepg.New(pool)
	identitySvc := identity.NewPostgresService(mds)
	reposSvc := repositories.NewService(mds)

	user, err := identitySvc.CreateUser(context.Background(), "alice@example.com", "S3cret!1234")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	pStore := persisted.NewCachedStore(persisted.NewPostgresStore(pool), cache.NewMemoryStore(nil))
	deps := gqlplane.Deps{
		Pools: gqlmw.Pools{Reader: mds, Primary: mds},
		Resolver: restapi.NewIdentityResolver(mds.Identity()),
		Limiter: ratelimit.NewMemoryLimiter(nil),
		Analyzer: cost.New(cost.DefaultLimits(), cost.DefaultFieldWeights()),
		Persisted: pStore,
		Resolvers: resolvers.Deps{
			Identity: identitySvc,
			Repositories: reposSvc,
			SVID: resolvers.AlwaysVerifiedSVID{},
		},
		Bucket: gqlmw.BucketParams{
			HumanCapacity: 10000, HumanRefillPerSec: 1000,
			AgentCapacity: 10000, AgentRefillPerSec: 1000,
		},
	}
	srv := httptest.NewServer(gqlplane.NewHandler(deps))
	t.Cleanup(srv.Close)

	tok := user.ID.String()

	// 1. Simple query.
	resp := post(t, srv.URL+"/graphql", tok, map[string]string{
		"query": `{ user(login: "alice@example.com") { id login email } }`,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("simple query status: %d", resp.StatusCode)
	}
	var env struct {
		Data       map[string]any  `json:"data"`
		Errors     []json.RawMessage `json:"errors"`
		Extensions map[string]any  `json:"extensions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(env.Errors) != 0 {
		t.Fatalf("query errors: %v", env.Errors)
	}
	u := env.Data["user"].(map[string]any)
	if u["email"] != "alice@example.com" {
		t.Errorf("email: %v", u["email"])
	}

	// 2. Cost rejection (depth-exceeded). Charges parse_cost.
	bad := strings.Repeat("{ a ", 12) + strings.Repeat("} ", 12)
	resp = post(t, srv.URL+"/graphql", tok, map[string]any{"query": bad})
	if resp.StatusCode != 200 {
		t.Errorf("depth-exceeded HTTP status: %d (GraphQL spec returns 200)", resp.StatusCode)
	}
	var rej map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rej)
	resp.Body.Close()
	errs := rej["errors"].([]any)
	if len(errs) == 0 || extCode(errs[0]) != "DEPTH_EXCEEDED" {
		t.Errorf("expected DEPTH_EXCEEDED, got %+v", rej)
	}

	// 3. Persisted register + execute, persistedDiscount surfaced.
	resp = post(t, srv.URL+"/graphql/persisted/register", tok, map[string]string{
		"query": `{ user(login: "alice@example.com") { id } }`,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("register: %d", resp.StatusCode)
	}
	var reg struct{ Hash string }
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()

	resp = post(t, srv.URL+"/graphql/persisted/"+reg.Hash, tok, map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("execute: %d", resp.StatusCode)
	}
	var penv struct {
		Data       map[string]any `json:"data"`
		Extensions map[string]any `json:"extensions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&penv)
	resp.Body.Close()
	if penv.Extensions["persistedDiscount"] == nil {
		t.Errorf("persistedDiscount missing")
	}

	// 4. Schema introspection — named subset present.
	resp = post(t, srv.URL+"/graphql", tok, map[string]string{
		"query": `{ __schema { queryType { name fields { name } } } }`,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("introspection: %d", resp.StatusCode)
	}
	var ienv struct {
		Data map[string]any `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ienv)
	resp.Body.Close()
	sch := ienv.Data["__schema"].(map[string]any)
	qt := sch["queryType"].(map[string]any)
	fields := qt["fields"].([]any)
	got := map[string]bool{}
	for _, f := range fields {
		got[f.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"repository", "user", "agent", "pullRequest", "organization"} {
		if !got[name] {
			t.Errorf("introspection missing %q", name)
		}
	}
}

func post(t *testing.T, url, tok string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
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

