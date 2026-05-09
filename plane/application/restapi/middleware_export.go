package restapi

import "net/http"

// RequestIDMiddleware returns the requestID middleware as an exported
// composable so other application-plane HTTP layers (e.g.
// plane/application/mcp) can reuse the exact same correlation-id
// shape without re-implementing it. The function returns a
// (next) -> next-wrapped handler-factory to match the chi/std-mux
// convention.
//
// Exposing this is intentional under ADR-019: MCP is in the
// application plane and reusing REST middlewares is the load-bearing
// "single source of truth" guarantee for auth + request-id semantics.
func RequestIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return requestID(next) }
}

// AuthMiddlewareForMCP exports the auth middleware. Same reasoning as
// RequestIDMiddleware; the suffix names the caller so other planes
// don't latch onto an "open" looking helper. Application plane only.
func AuthMiddlewareForMCP(resolver PrincipalResolver) func(http.Handler) http.Handler {
	return authMiddleware(resolver)
}
