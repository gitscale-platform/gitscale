package restapi

import (
	"net/http"

	"github.com/google/uuid"
)

// requestIDHeader is the canonical correlation header used both as input
// (passthrough from upstream proxies) and output (echoed back to the client
// and stamped into structured logs).
const requestIDHeader = "X-Request-Id"

// requestID is the outermost middleware. It either passes through a
// caller-supplied X-Request-Id (parsed as UUID) or generates a fresh UUID,
// then writes the value to the response header and request context.
//
// The middleware never fails open with an empty request id — every request
// carries a stable identifier downstream consumers can correlate.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" || !looksLikeUUID(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// looksLikeUUID is a cheap validator: rejects values that aren't valid
// UUID v4-ish strings so an attacker can't smuggle arbitrary content via
// the request-id header into structured logs.
func looksLikeUUID(s string) bool {
	if _, err := uuid.Parse(s); err != nil {
		return false
	}
	return true
}
