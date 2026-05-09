package restapi

import (
	"log/slog"
	"net/http"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// Deps bundles the inputs to NewRouter. All fields are required except
// Logger (defaulted to slog.Default()).
type Deps struct {
	Identity     identity.Service
	Repositories repositories.Service
	Resolver     PrincipalResolver
	Limiter      ratelimit.RateLimiter
	RateConfig   RateConfig
	Logger       *slog.Logger
}

// NewRouter composes the HTTP handler tree.
//
// Middleware order (outer → inner): RequestID → Auth → RateLimit → mux.
//
// Why: request-id is needed in the very first log line a handler may emit,
// including auth-failure logs. Auth runs before rate-limit so an
// unauthenticated request never consumes a real principal's token bucket
// (the bucket key is derived from the resolved principal id; without auth
// there is no principal to charge against).
func NewRouter(d Deps) http.Handler {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()

	idH := &identityHandlers{svc: d.Identity, logger: logger}
	repoH := &reposHandlers{svc: d.Repositories, logger: logger}

	mux.HandleFunc("POST /v1/users", idH.createUser)
	mux.HandleFunc("GET /v1/users/{id}", idH.getUser)
	mux.HandleFunc("POST /v1/agents", idH.createAgent)
	mux.HandleFunc("GET /v1/agents/{id}", idH.getAgent)
	mux.HandleFunc("DELETE /v1/agents/{id}", idH.revokeAgent)
	mux.HandleFunc("PATCH /v1/agents/{id}/permissions", idH.updatePerms)

	mux.HandleFunc("POST /v1/repos", repoH.createRepo)
	mux.HandleFunc("GET /v1/repos/{id}", repoH.getRepo)
	mux.HandleFunc("DELETE /v1/repos/{id}", repoH.deleteRepo)
	mux.HandleFunc("GET /v1/orgs/{org}/repos", repoH.listOrgRepos)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	var handler http.Handler = mux
	handler = rateLimitMiddleware(d.Limiter, d.RateConfig)(handler)
	handler = authMiddleware(d.Resolver)(handler)
	handler = requestID(handler)
	return handler
}
