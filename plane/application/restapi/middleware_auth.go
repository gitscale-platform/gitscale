package restapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// PrincipalResolver resolves a bearer token to a Principal. Production
// implementations call identity.Service.LookupIdentityForCache plus a
// kind→Principal adapter; tests inject a fixed-token map.
type PrincipalResolver interface {
	Resolve(ctx context.Context, bearer string) (Principal, error)
}

// ErrInvalidToken is returned by a PrincipalResolver when the supplied
// bearer cannot be mapped to a known principal. It is mapped to a 401
// response by the auth middleware.
var ErrInvalidToken = errors.New("restapi: invalid bearer token")

// authSkipPaths are the URL paths that bypass auth. Only /healthz today;
// adding to this list is a public-API change.
var authSkipPaths = map[string]struct{}{
	"/healthz": {},
}

// authMiddleware validates the Authorization: Bearer header and stamps the
// resolved Principal onto r.Context.
//
// Empty header or unresolved token → 401. The handler chain never sees an
// authenticated request without a Principal in context.
func authMiddleware(resolver PrincipalResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, skip := authSkipPaths[r.URL.Path]; skip {
				next.ServeHTTP(w, r)
				return
			}

			h := r.Header.Get("Authorization")
			if h == "" {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing Authorization header")
				return
			}
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "expected 'Bearer <token>'")
				return
			}
			token := strings.TrimSpace(h[len(prefix):])
			if token == "" {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "empty bearer token")
				return
			}
			principal, err := resolver.Resolve(r.Context(), token)
			if err != nil || principal == nil {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "invalid bearer token")
				return
			}
			ctx := WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
