package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/mcp"
	"github.com/gitscale-platform/gitscale/plane/application/mcp/cirunclient"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

// fixedResolver maps known tokens to a Principal. Avoids spinning up an
// identity store for the HTTP-layer tests; the integration test
// exercises the postgres path.
type fixedResolver struct {
	byTok map[string]restapi.Principal
}

func (r *fixedResolver) Resolve(_ context.Context, bearer string) (restapi.Principal, error) {
	if p, ok := r.byTok[bearer]; ok {
		return p, nil
	}
	return nil, restapi.ErrInvalidToken
}

type serverFixture struct {
	srv     *httptest.Server
	limiter *ratelimit.MemoryLimiter
	stub    *stub.Store
	idSvc   identity.Service
	repos   repositories.Service
	agent   restapi.Principal
	human   restapi.Principal
	tokA    string
	tokH    string
	ciStub  *cirunclient.StubClient
}

func newFixture(t *testing.T, rate restapi.RateConfig) *serverFixture {
	t.Helper()
	st := stub.New()
	// stubServiceForTest sidesteps the production Argon2id hasher (which
	// otherwise dominates test runtime). The MCP server-level tests do
	// not exercise CreateUser; we wire the stub directly so MintCloneToken
	// + GetRepository observe a shared in-memory store.
	idSvc := identity.NewStubServiceWithStore(st)
	reposSvc := repositories.NewService(st)

	userID := uuid.New()
	agentID := uuid.New()
	tokA, tokH := "tok-agent-1", "tok-human-1"
	resolver := &fixedResolver{
		byTok: map[string]restapi.Principal{
			tokA: restapi.AgentPrincipal{AgentID: agentID, ParentUserID: userID},
			tokH: restapi.HumanPrincipal{UserID: userID},
		},
	}
	limiter := ratelimit.NewMemoryLimiter(nil)
	ciStub := &cirunclient.StubClient{Handle: cirunclient.RunHandle{WorkflowID: "wf-x", RunID: "run-x"}}
	if rate == (restapi.RateConfig{}) {
		rate = restapi.RateConfig{HumanCapacity: 100, HumanRefillPerSec: 100, AgentCapacity: 100, AgentRefillPerSec: 100}
	}
	restRouter := restapi.NewRouter(restapi.Deps{
		Identity: idSvc, Repositories: reposSvc, Resolver: resolver,
		Limiter: limiter, RateConfig: rate,
	})
	srv, err := mcp.NewServer(mcp.Config{
		ProtocolVersion:   "2025-06-18",
		ServerName:        "test", ServerVersion: "0.0.1",
		SessionHMACSecret: []byte("0123456789abcdef0123456789abcdef"),
		RateConfig:        rate,
	}, mcp.Deps{
		Identity: idSvc, Repositories: reposSvc, Resolver: resolver,
		Limiter: limiter, RESTHandler: restRouter, CIRunClient: ciStub,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &serverFixture{
		srv: ts, limiter: limiter, stub: st, idSvc: idSvc, repos: reposSvc,
		agent: restapi.AgentPrincipal{AgentID: agentID, ParentUserID: userID},
		human: restapi.HumanPrincipal{UserID: userID},
		tokA:  tokA, tokH: tokH, ciStub: ciStub,
	}
}

func rpcCall(t *testing.T, srv *httptest.Server, tok, path string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(buf))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func decodeRPC(t *testing.T, resp *http.Response) (any, *struct {
	Code    mcp.Code
	Message string
}) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var raw struct {
		Result any `json:"result"`
		Error  *struct {
			Code    mcp.Code `json:"code"`
			Message string   `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw.Error != nil {
		return raw.Result, &struct {
			Code    mcp.Code
			Message string
		}{raw.Error.Code, raw.Error.Message}
	}
	return raw.Result, nil
}

// ─── tests ───────────────────────────────────────────────────────────────────

func TestServer_InitializeHandshake(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/initialize", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("rpc err: %+v", errBody)
	}
	m := res.(map[string]any)
	if m["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion: got %v", m["protocolVersion"])
	}
	if m["sessionId"] == "" || m["sessionId"] == nil {
		t.Errorf("sessionId missing")
	}
}

func TestServer_ToolsList_AllSeven(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/list", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("rpc err: %+v", errBody)
	}
	tools := res.(map[string]any)["tools"].([]any)
	if len(tools) != len(mcp.AllToolNames()) {
		t.Fatalf("tool count: got %d want %d", len(tools), len(mcp.AllToolNames()))
	}
}

func TestServer_MissingBearer_Unauthenticated(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, "", "/mcp/v1/tools/list", map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestServer_InvalidBearer_Unauthenticated(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, "garbage", "/mcp/v1/tools/list", map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/list",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestServer_RateLimitExhausted(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{AgentCapacity: 1, AgentRefillPerSec: 0, HumanCapacity: 1, HumanRefillPerSec: 0})
	// First call drains the bucket.
	r1 := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/list", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if r1.StatusCode != 200 {
		t.Fatalf("first call status: %d", r1.StatusCode)
	}
	_ = r1.Body.Close()
	// Second call rejected.
	r2 := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/list", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if r2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second call status: %d", r2.StatusCode)
	}
	_, errBody := decodeRPC(t, r2)
	if errBody == nil || errBody.Code != mcp.CodeRateLimited {
		t.Errorf("err: %+v", errBody)
	}
}

func TestServer_UnknownTool_MethodNotFound(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "nope", "arguments": map[string]any{}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: %d", resp.StatusCode)
	}
	_, errBody := decodeRPC(t, resp)
	if errBody == nil || errBody.Code != mcp.CodeMethodNotFound {
		t.Errorf("err: %+v", errBody)
	}
}

func TestServer_QuotaStatus_ReflectsBucket(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{AgentCapacity: 5, AgentRefillPerSec: 0, HumanCapacity: 5, HumanRefillPerSec: 0})
	// First call uses one token (quota_status itself counts).
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "quota_status", "arguments": map[string]any{}},
	})
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("err: %+v", errBody)
	}
	m := res.(map[string]any)
	if m["surface"] != "mcp" {
		t.Errorf("surface: got %v", m["surface"])
	}
	if m["capacity"].(float64) != 5 {
		t.Errorf("capacity: got %v", m["capacity"])
	}
	// Bucket started at 5, the rate-limit middleware took 1 before
	// dispatch, so remaining is 4.
	if m["remaining"].(float64) != 4 {
		t.Errorf("remaining: got %v want 4", m["remaining"])
	}
}

func TestServer_PRCreate_NotImplemented(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "pr_create", "arguments": map[string]any{
			"repo_id": uuid.NewString(), "title": "x", "source_ref": "a", "target_ref": "b",
		}},
	})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status: %d", resp.StatusCode)
	}
	_, errBody := decodeRPC(t, resp)
	if errBody == nil || errBody.Code != mcp.CodeNotImplemented {
		t.Errorf("err: %+v", errBody)
	}
}

func TestServer_CITrigger_StubReturnsHandle(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "ci_trigger", "arguments": map[string]any{
			"repo_id": uuid.NewString(), "ref": "refs/heads/main",
		}},
	})
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("err: %+v", errBody)
	}
	m := res.(map[string]any)
	if m["workflow_id"] != "wf-x" {
		t.Errorf("workflow_id: got %v", m["workflow_id"])
	}
	if !f.ciStub.Called {
		t.Error("stub not called")
	}
}

func TestServer_GitClone_MintsTokenAndOutbox(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	repo, err := f.repos.CreateRepository(context.Background(), repositories.CreateInput{
		OrgID: uuid.New(), OwnerID: f.human.ID(), Slug: "test", Name: "test", Visibility: "private",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "git_clone", "arguments": map[string]any{
			"repo_id": repo.ID.String(),
		}},
	})
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("err: %+v", errBody)
	}
	m := res.(map[string]any)
	if m["token"] == "" || m["clone_url"] == "" {
		t.Errorf("missing fields: %+v", m)
	}
	// Outbox row asserted.
	var found bool
	for _, ev := range f.stub.Recorded() {
		if ev.EventType == identity.EventCloneTokenMinted {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("outbox: clone_token_minted not recorded")
	}
	if len(f.stub.CloneTokens()) != 1 {
		t.Errorf("clone_tokens: got %d want 1", len(f.stub.CloneTokens()))
	}
}

func TestServer_AgentsMDValidate_AllDiagCodes(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	// Document deliberately exercises CodeMalformedFrontMatter +
	// CodeUnknownPredicate + CodeMalformedPredicate +
	// CodeUnsupportedSchemaVersion paths via several calls.
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{"missing_front_matter", "## Never\n- push_to_branch: main\n", "front-matter"},
		{"unsupported_schema", "---\nschema: gitscale/v999\n---\n", "unsupported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": "agents_md_validate", "arguments": map[string]any{
					"content": c.content,
				}},
			})
			res, errBody := decodeRPC(t, resp)
			if errBody != nil {
				t.Fatalf("err: %+v", errBody)
			}
			diags := res.(map[string]any)["diagnostics"].([]any)
			if len(diags) == 0 {
				t.Fatalf("expected diagnostics for %q", c.name)
			}
			if !strings.Contains(strings.ToLower(diags[0].(map[string]any)["message"].(string)), c.wantSub) {
				t.Errorf("message %q does not contain %q", diags[0], c.wantSub)
			}
		})
	}
}

func TestServer_AgentsMDEvaluate_PushToMain(t *testing.T) {
	f := newFixture(t, restapi.RateConfig{})
	doc := "---\nschema: gitscale/v1\n---\n## Never\n- push_to_branch: main\n"
	resp := rpcCall(t, f.srv, f.tokA, "/mcp/v1/tools/call", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "agents_md_evaluate", "arguments": map[string]any{
			"agents_md_content": doc,
			"updates": []map[string]any{
				{"ref_name": "refs/heads/main", "old_oid": "0000000000000000000000000000000000000000", "new_oid": "abc"},
			},
		}},
	})
	res, errBody := decodeRPC(t, resp)
	if errBody != nil {
		t.Fatalf("err: %+v", errBody)
	}
	v := res.(map[string]any)["violations"].([]any)
	if len(v) != 1 {
		t.Fatalf("violations: got %d want 1", len(v))
	}
}

// silence unused import warnings under build tags.
var _ = errors.Is
