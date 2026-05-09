// Package restclient is the in-process loopback adapter from the MCP
// layer into the REST handler tree. It is internal to the MCP package
// and not exported as a public API surface.
//
// The loopback bypasses TCP entirely: an http.Request is dispatched
// against the REST router via httptest.ResponseRecorder. The request
// is marked with restapi.WithInternalCall so the REST rate-limit
// middleware skips its bucket draw — the MCP rate-limit middleware
// has already metered the call against SurfaceMCP, so a second draw
// here would double-bill the principal.
package restclient

import (
	"net/http"
	"net/http/httptest"

	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

// Client wraps a REST http.Handler for in-process dispatch.
type Client struct {
	handler http.Handler
}

// New constructs a loopback client over h. h must be the value
// returned by restapi.NewRouter (or an equivalent handler tree); the
// loopback contract relies on the REST rate-limit middleware honouring
// restapi.IsInternalCall.
func New(h http.Handler) *Client { return &Client{handler: h} }

// Do dispatches r against the REST handler and returns the recorder.
// The X-MCP-Internal header is informational only — the load-bearing
// signal is the context value set by restapi.WithInternalCall, which
// cannot be forged by an external HTTP caller.
func (c *Client) Do(r *http.Request) *httptest.ResponseRecorder {
	r.Header.Set("X-MCP-Internal", "1")
	r2 := r.WithContext(restapi.WithInternalCall(r.Context()))
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, r2)
	return rec
}
