package restclient_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/mcp/internal/restclient"
	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// TestRestClient_SetsInternalCallContext proves the loopback marks the
// request via restapi.WithInternalCall — the load-bearing signal the
// REST rate-limit middleware checks. The header alone is not trusted.
func TestRestClient_SetsInternalCallContext(t *testing.T) {
	var sawInternal bool
	var sawHeader string
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInternal = restapi.IsInternalCall(r.Context())
		sawHeader = r.Header.Get("X-MCP-Internal")
	})
	c := restclient.New(h)
	req := httptest.NewRequest("GET", "/v1/anything", nil)
	c.Do(req)
	if !sawInternal {
		t.Error("expected restapi.IsInternalCall to be true via context")
	}
	if sawHeader != "1" {
		t.Errorf("X-MCP-Internal header: got %q want %q", sawHeader, "1")
	}
}

// TestExternalRequest_HeaderOnly_NotTrusted proves an external HTTP
// request that carries the X-MCP-Internal header is NOT treated as
// internal — IsInternalCall returns false because the context value
// is missing.
func TestExternalRequest_HeaderOnly_NotTrusted(t *testing.T) {
	var sawInternal bool
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInternal = restapi.IsInternalCall(r.Context())
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/anything", nil)
	req.Header.Set("X-MCP-Internal", "1") // would-be spoof
	h.ServeHTTP(rec, req)
	if sawInternal {
		t.Error("external request with X-MCP-Internal header was treated as internal — security bug")
	}
}
