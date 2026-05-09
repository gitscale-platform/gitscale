package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

// fixedResolver maps tokens → principals for tests. Unknown tokens fail.
type fixedResolver struct {
	m map[string]Principal
}

func (f *fixedResolver) Resolve(_ context.Context, bearer string) (Principal, error) {
	if p, ok := f.m[bearer]; ok {
		return p, nil
	}
	return nil, ErrInvalidToken
}

// memLimiter is a tiny in-memory bucket for tests. We don't reuse
// ratelimit.MemoryLimiter to keep the test free of timing assumptions —
// tokens never refill, they're pre-loaded.
type memLimiter struct {
	mu      sync.Mutex
	buckets map[string]float64
}

func newMemLimiter() *memLimiter { return &memLimiter{buckets: map[string]float64{}} }

func (l *memLimiter) Take(_ context.Context, key string, capacity, _, n float64) (bool, float64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.buckets[key]
	if !ok {
		cur = capacity
	}
	if cur < n {
		l.buckets[key] = cur
		return false, cur, nil
	}
	cur -= n
	l.buckets[key] = cur
	return true, cur, nil
}

// brokenLimiter always errors so we can prove the middleware fails closed.
type brokenLimiter struct{}

func (brokenLimiter) Take(_ context.Context, _ string, _, _, _ float64) (bool, float64, error) {
	return false, 0, errors.New("limiter exploded")
}

func newTestRouter(t *testing.T, l ratelimit.RateLimiter, cfg RateConfig, principals map[string]Principal) (*httptest.Server, identity.Service, repositories.Service) {
	t.Helper()
	store := stub.New()
	identitySvc := identity.NewStubService()
	// repositories.Service uses the same backing stub.Store so writes are
	// observable across services. We re-use the stub's MetadataStore handle.
	reposSvc := repositories.NewService(store)
	resolver := &fixedResolver{m: principals}
	r := NewRouter(Deps{
		Identity:     identitySvc,
		Repositories: reposSvc,
		Resolver:     resolver,
		Limiter:      l,
		RateConfig:   cfg,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, identitySvc, reposSvc
}

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

func doReq(t *testing.T, method, url, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestHealthz_skipsAuth(t *testing.T) {
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, nil)
	resp := doReq(t, "GET", srv.URL+"/healthz", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuth_missingBearer_401(t *testing.T) {
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, nil)
	resp := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"unauthenticated"`) {
		t.Errorf("body missing code: %s", body)
	}
	// Even a 401 must echo a request id.
	if resp.Header.Get(requestIDHeader) == "" {
		t.Error("X-Request-Id missing on 401")
	}
}

func TestAuth_invalidToken_401(t *testing.T) {
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, nil)
	resp := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "garbage", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuth_humanGetMissingUser_404(t *testing.T) {
	humanID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok-human": HumanPrincipal{UserID: humanID},
	})
	resp := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "tok-human", nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestCreateUser_201_andLocation(t *testing.T) {
	humanID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok-human": HumanPrincipal{UserID: humanID},
	})
	body := mustJSON(t, map[string]string{"email": "alice@example.com", "credential": "S3cret!1234"})
	resp := doReq(t, "POST", srv.URL+"/v1/users", "tok-human", body)
	if resp.StatusCode != 201 {
		dump, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body=%s", resp.StatusCode, dump)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/v1/users/") {
		t.Errorf("Location header missing/bad: %q", loc)
	}
}

func TestCreateUser_byAgent_403(t *testing.T) {
	agentID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok-agent": AgentPrincipal{AgentID: agentID, ParentUserID: uuid.New()},
	})
	body := mustJSON(t, map[string]string{"email": "x@y.z", "credential": "pw"})
	resp := doReq(t, "POST", srv.URL+"/v1/users", "tok-agent", body)
	if resp.StatusCode != 403 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestCreateAgent_byOtherUser_403(t *testing.T) {
	humanA := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok-a": HumanPrincipal{UserID: humanA},
	})
	body := mustJSON(t, map[string]any{
		"parent_user_id":   uuid.NewString(), // not humanA
		"display_name":     "x",
		"permission_scope": []string{"repo:read"},
	})
	resp := doReq(t, "POST", srv.URL+"/v1/agents", "tok-a", body)
	if resp.StatusCode != 403 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestRateLimit_429_andRetryAfter(t *testing.T) {
	humanID := uuid.New()
	limiter := newMemLimiter()
	cfg := RateConfig{HumanCapacity: 1, HumanRefillPerSec: 0}
	srv, _, _ := newTestRouter(t, limiter, cfg, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	// First call consumes the only token.
	r1 := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "tok", nil)
	if r1.StatusCode != 404 { // user not found is fine; rate-limit allowed
		t.Fatalf("expected 404 got %d", r1.StatusCode)
	}
	r2 := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "tok", nil)
	if r2.StatusCode != 429 {
		t.Fatalf("expected 429 got %d", r2.StatusCode)
	}
	if r2.Header.Get("Retry-After") == "" {
		t.Error("Retry-After missing")
	}
}

func TestRateLimit_failsClosedOnBackendError(t *testing.T) {
	humanID := uuid.New()
	cfg := RateConfig{HumanCapacity: 10, HumanRefillPerSec: 1}
	srv, _, _ := newTestRouter(t, brokenLimiter{}, cfg, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	resp := doReq(t, "GET", srv.URL+"/v1/users/"+uuid.NewString(), "tok", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("expected 500 got %d", resp.StatusCode)
	}
}

func TestRequestID_passthrough(t *testing.T) {
	humanID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	want := uuid.NewString()
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-Id", want)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Header.Get(requestIDHeader) != want {
		t.Errorf("X-Request-Id: got %q want %q", resp.Header.Get(requestIDHeader), want)
	}
}

func TestRequestID_garbageInputReplaced(t *testing.T) {
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, nil)
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-Id", "<script>alert(1)</script>")
	resp, _ := http.DefaultClient.Do(req)
	got := resp.Header.Get(requestIDHeader)
	if got == "<script>alert(1)</script>" {
		t.Errorf("garbage X-Request-Id was not replaced: %q", got)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Errorf("X-Request-Id should be UUID, got %q (%v)", got, err)
	}
}

func TestDeleteRepo_501(t *testing.T) {
	humanID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	resp := doReq(t, "DELETE", srv.URL+"/v1/repos/"+uuid.NewString(), "tok", nil)
	if resp.StatusCode != 501 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestCreateAndGetRepo_HumanFlow(t *testing.T) {
	humanID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	orgID := uuid.New()
	body := mustJSON(t, map[string]string{
		"org_id":     orgID.String(),
		"slug":       "demo-repo",
		"name":       "Demo Repo",
		"visibility": "private",
	})
	resp := doReq(t, "POST", srv.URL+"/v1/repos", "tok", body)
	if resp.StatusCode != 201 {
		dump, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status: %d body=%s", resp.StatusCode, dump)
	}
	var created repositoryResponse
	_ = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()

	resp = doReq(t, "GET", srv.URL+"/v1/repos/"+created.ID, "tok", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get status: %d", resp.StatusCode)
	}
	var got repositoryResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.ID != created.ID || got.Slug != "demo-repo" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestCreateRepo_byAgent_403(t *testing.T) {
	agentID := uuid.New()
	srv, _, _ := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok": AgentPrincipal{AgentID: agentID, ParentUserID: uuid.New()},
	})
	body := mustJSON(t, map[string]string{
		"org_id": uuid.NewString(), "slug": "x", "name": "X",
	})
	resp := doReq(t, "POST", srv.URL+"/v1/repos", "tok", body)
	if resp.StatusCode != 403 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestListOrgRepos_paginates(t *testing.T) {
	humanID := uuid.New()
	srv, _, repoSvc := newTestRouter(t, newMemLimiter(), RateConfig{}, map[string]Principal{
		"tok": HumanPrincipal{UserID: humanID},
	})
	orgID := uuid.New()
	const total = 25
	for i := 0; i < total; i++ {
		_, err := repoSvc.CreateRepository(context.Background(), repositories.CreateInput{
			OrgID:   orgID,
			OwnerID: humanID,
			Slug:    "repo-" + strings.Repeat("a", 1) + uuid.NewString()[:8],
			Name:    "n",
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	seen := map[string]struct{}{}
	cursor := ""
	const pageSize = 10
	pages := 0
	for {
		url := srv.URL + "/v1/orgs/" + orgID.String() + "/repos?limit=10"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp := doReq(t, "GET", url, "tok", nil)
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("page %d status: %d body=%s", pages, resp.StatusCode, body)
		}
		var lr listResponse
		_ = json.NewDecoder(resp.Body).Decode(&lr)
		_ = resp.Body.Close()
		for _, it := range lr.Items {
			if _, dup := seen[it.ID]; dup {
				t.Errorf("duplicate id across pages: %s", it.ID)
			}
			seen[it.ID] = struct{}{}
		}
		pages++
		if lr.NextCursor == "" {
			break
		}
		cursor = lr.NextCursor
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		_ = pageSize
	}
	if len(seen) != total {
		t.Errorf("seen %d items, want %d", len(seen), total)
	}
}
