//go:build integration

package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	storepg "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPostgres(t *testing.T) *pgxpool.Pool {
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

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
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

func TestRestAPI_FullFlow_postgres(t *testing.T) {
	pool := setupPostgres(t)
	mds := storepg.New(pool)
	identitySvc := identity.NewPostgresService(mds)
	reposSvc := repositories.NewService(mds)

	// Seed a user up-front so we know the resolver token.
	user, err := identitySvc.CreateUser(context.Background(), "operator@example.com", "S3cret!1234")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Seed an org row so the FK soft-ref is realistic (not enforced by SQL,
	// but the slug+org_id UNIQUE applies).
	orgID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO identity.organisations (id, slug) VALUES ($1, $2)`,
		orgID, "acme",
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	resolver := restapi.NewIdentityResolver(mds.Identity())
	router := restapi.NewRouter(restapi.Deps{
		Identity:     identitySvc,
		Repositories: reposSvc,
		Resolver:     resolver,
		Limiter:      ratelimit.NewMemoryLimiter(nil),
		RateConfig:   restapi.RateConfig{HumanCapacity: 100, HumanRefillPerSec: 100, AgentCapacity: 100, AgentRefillPerSec: 100},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	tok := user.ID.String()

	// /healthz: no auth.
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// GET /v1/users/{id}
	resp = doAuth(t, "GET", srv.URL+"/v1/users/"+user.ID.String(), tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get user: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// POST /v1/agents
	body := mustJSON(t, map[string]any{
		"parent_user_id":   user.ID.String(),
		"display_name":     "ci-agent",
		"permission_scope": []string{"repo:read"},
	})
	resp = doAuth(t, "POST", srv.URL+"/v1/agents", tok, body)
	if resp.StatusCode != 201 {
		dump, _ := io.ReadAll(resp.Body)
		t.Fatalf("create agent: %d %s", resp.StatusCode, dump)
	}
	var agent struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&agent)
	resp.Body.Close()

	// POST /v1/repos
	body = mustJSON(t, map[string]string{
		"org_id":     orgID.String(),
		"slug":       "demo",
		"name":       "Demo",
		"visibility": "private",
	})
	resp = doAuth(t, "POST", srv.URL+"/v1/repos", tok, body)
	if resp.StatusCode != 201 {
		dump, _ := io.ReadAll(resp.Body)
		t.Fatalf("create repo: %d %s", resp.StatusCode, dump)
	}
	var repo struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&repo)
	resp.Body.Close()

	// outbox row was written for repo.created.
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM repositories.repositories_outbox WHERE event_type = 'repositories.repository_created' AND aggregate_id = $1`,
		repo.ID,
	).Scan(&n); err != nil || n != 1 {
		t.Errorf("repo outbox: count=%d err=%v", n, err)
	}

	// GET /v1/repos/{id}
	resp = doAuth(t, "GET", srv.URL+"/v1/repos/"+repo.ID, tok, nil)
	if resp.StatusCode != 200 {
		t.Errorf("get repo: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// GET /v1/orgs/{org}/repos
	resp = doAuth(t, "GET", srv.URL+"/v1/orgs/"+orgID.String()+"/repos", tok, nil)
	if resp.StatusCode != 200 {
		t.Errorf("list repos: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE /v1/agents/{id}
	resp = doAuth(t, "DELETE", srv.URL+"/v1/agents/"+agent.ID, tok, mustJSON(t, map[string]string{"reason": "test"}))
	if resp.StatusCode != 204 {
		t.Errorf("revoke agent: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRestAPI_unauthenticated_401(t *testing.T) {
	pool := setupPostgres(t)
	mds := storepg.New(pool)
	router := restapi.NewRouter(restapi.Deps{
		Identity:     identity.NewPostgresService(mds),
		Repositories: repositories.NewService(mds),
		Resolver:     restapi.NewIdentityResolver(mds.Identity()),
		Limiter:      ratelimit.NewMemoryLimiter(nil),
		RateConfig:   restapi.RateConfig{HumanCapacity: 10, HumanRefillPerSec: 10},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/users/" + uuid.NewString())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func doAuth(t *testing.T, method, url, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}
