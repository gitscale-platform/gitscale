package graphql

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/persisted"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/resolvers"
	restapi "github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
	"github.com/google/uuid"
)

// MaxRequestBodyBytes caps GraphQL request bodies at 1 MiB. The cost
// analyser is the primary DoS guard, but a body cap closes the obvious
// pre-parse memory exhaustion vector.
const MaxRequestBodyBytes = 1 << 20

// Deps wires the GraphQL handler. Mirrors restapi.Deps in spirit; ADR-017
// swap surfaces (Pools, Persisted, Limiter) are all injected.
type Deps struct {
	Pools     middleware.Pools
	Resolver  restapi.PrincipalResolver
	Limiter   ratelimit.RateLimiter
	Analyzer  *cost.Analyzer
	Persisted persisted.Store
	Resolvers resolvers.Deps
	Bucket    middleware.BucketParams
	Logger    *slog.Logger
}

// NewHandler composes the GraphQL HTTP handler tree.
//
// Routes:
//
//	POST /graphql                       — ad-hoc query
//	POST /graphql/persisted/register    — register a persisted query
//	POST /graphql/persisted/{hash}      — execute a persisted query
//	GET  /graphql/healthz               — liveness probe
//
// Middleware order outer→inner:
//
//	RequestID  → Auth  → Body-cap & route dispatch  → Cost-analyse  →
//	Cost-meter (charge)  → Resolver dispatch
//
// Cost-meter charges *after* analysis so rejected queries pay the cheaper
// parse_cost; auth runs before parsing so unauthenticated traffic never
// reaches the parser; persisted-query routes hit the same downstream
// pipeline once the body has been resolved to text.
func NewHandler(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	mux := http.NewServeMux()
	h := &handler{deps: d}

	mux.HandleFunc("POST /graphql", h.serveAdHoc)
	mux.HandleFunc("POST /graphql/persisted/register", h.servePersistedRegister)
	mux.HandleFunc("POST /graphql/persisted/{hash}", h.servePersistedExecute)
	mux.HandleFunc("GET /graphql/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return requestIDMiddleware(authMiddleware(d, mux))
}

type handler struct {
	deps Deps
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		} else if _, err := uuid.Parse(id); err != nil {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := middleware.WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authMiddleware enforces bearer-token presence except for /graphql/healthz.
// On success it stamps a middleware.Principal onto the request context.
//
// Introspection requests (`{ __schema … }`) MAY be allowed without auth
// per the spec to ease tooling discovery; that bypass is opt-in via a
// header `X-Graphql-Anon-Introspection: 1` so production deployments can
// disable it by stripping the header at the edge.
func authMiddleware(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			writeGraphqlError(w, r, http.StatusUnauthorized, NewError(CodeUnauthenticated, "missing bearer token"))
			return
		}
		token := strings.TrimSpace(auth[len("Bearer "):])
		if token == "" {
			writeGraphqlError(w, r, http.StatusUnauthorized, NewError(CodeUnauthenticated, "empty bearer token"))
			return
		}
		rp, err := d.Resolver.Resolve(r.Context(), token)
		if err != nil || rp == nil {
			writeGraphqlError(w, r, http.StatusUnauthorized, NewError(CodeUnauthenticated, "invalid bearer token"))
			return
		}
		p := middleware.Principal{ID: rp.ID()}
		switch rp.Kind() {
		case restapi.PrincipalHuman:
			p.Kind = middleware.PrincipalHuman
		case restapi.PrincipalAgent:
			p.Kind = middleware.PrincipalAgent
			if ap, ok := rp.(restapi.AgentPrincipal); ok {
				p.ParentUserID = ap.ParentUserID
			}
		}
		ctx := middleware.WithPrincipal(r.Context(), p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestEnvelope is the GraphQL-over-HTTP request shape.
type requestEnvelope struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// responseEnvelope is the GraphQL response.
type responseEnvelope struct {
	Data       map[string]any `json:"data"`
	Errors     []Error        `json:"errors,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func (h *handler) serveAdHoc(w http.ResponseWriter, r *http.Request) {
	env, err := readRequest(r)
	if err != nil {
		writeGraphqlError(w, r, http.StatusBadRequest, NewError(CodeValidationFailed, err.Error()))
		return
	}
	h.execute(w, r, env, false)
}

func (h *handler) servePersistedRegister(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes))
	if err != nil {
		writeGraphqlError(w, r, http.StatusBadRequest, NewError(CodeValidationFailed, "read body: "+err.Error()))
		return
	}
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Query == "" {
		writeGraphqlError(w, r, http.StatusBadRequest, NewError(CodeValidationFailed, "missing query"))
		return
	}
	hash := persisted.HashFor(in.Query)
	p := middleware.PrincipalFrom(r.Context())
	if err := h.deps.Persisted.Put(r.Context(), hash, in.Query, p.ID); err != nil {
		code, msg := MapErr(r.Context(), h.deps.Logger, err)
		writeGraphqlError(w, r, statusForCode(code), NewError(code, msg))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"hash": hash})
}

func (h *handler) servePersistedExecute(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if hash == "" {
		writeGraphqlError(w, r, http.StatusBadRequest, NewError(CodeValidationFailed, "missing hash"))
		return
	}
	q, err := h.deps.Persisted.Get(r.Context(), hash)
	if err != nil {
		code, msg := MapErr(r.Context(), h.deps.Logger, err)
		writeGraphqlError(w, r, statusForCode(code), NewError(code, msg))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes))
	if err != nil {
		writeGraphqlError(w, r, http.StatusBadRequest, NewError(CodeValidationFailed, "read body: "+err.Error()))
		return
	}
	env := requestEnvelope{Query: q}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &env)
		env.Query = q // never let the body override the persisted text
	}
	h.execute(w, r, env, true)
}

// execute runs cost analysis, charges the bucket, and dispatches the
// resolver tree.
func (h *handler) execute(w http.ResponseWriter, r *http.Request, env requestEnvelope, persistedHit bool) {
	ctx := r.Context()
	princ := middleware.PrincipalFrom(ctx)
	pkind := pkindToCost(princ.Kind)

	// 1. Cost analysis (pre-execution).
	c, analyseErr := h.deps.Analyzer.Analyze(env.Query, env.OperationName, env.Variables, pkind, persistedHit)

	// 2. Cost meter — accepted vs rejected.
	meter := &middleware.CostMeter{Limiter: h.deps.Limiter, Params: h.deps.Bucket}
	if analyseErr != nil {
		// Charge the cheaper parse_cost but ignore RATE_LIMITED here so
		// the caller still sees the (more actionable) cost-rejection
		// reason. A real-world deployment may prefer to invert that — log
		// both and pick the more severe — but for v1 the cost-rejection
		// signal is the higher-information error.
		_ = meter.Charge(ctx, princ, middleware.TokensFor(c, false))

		ext := map[string]any{}
		ext["cost"] = c.Complexity
		ext["depth"] = c.Depth
		if persistedHit {
			ext["persistedDiscount"] = h.deps.Analyzer.Limits.PersistedDiscount
		}
		code, msg := MapErr(ctx, h.deps.Logger, analyseErr)
		gerr := Error{Message: msg, Extensions: map[string]any{"code": string(code)}}
		for k, v := range ext {
			gerr.Extensions[k] = v
		}
		gerr = gerr.withRequestID(middleware.RequestIDFrom(ctx))
		writeGraphqlBody(w, http.StatusOK, responseEnvelope{Errors: []Error{gerr}})
		return
	}
	if err := meter.Charge(ctx, princ, middleware.TokensFor(c, true)); err != nil {
		if middleware.IsRateLimited(err) {
			var rl middleware.ErrRateLimited
			_ = errors.As(err, &rl)
			w.Header().Set("Retry-After", strconv.Itoa(rl.RetrySeconds))
			gerr := Error{
				Message: "rate limit exceeded",
				Extensions: map[string]any{
					"code":               string(CodeRateLimited),
					"retryAfterSeconds":  rl.RetrySeconds,
					"cost":               c.Complexity,
				},
			}
			gerr = gerr.withRequestID(middleware.RequestIDFrom(ctx))
			writeGraphqlBody(w, http.StatusOK, responseEnvelope{Errors: []Error{gerr}})
			return
		}
		// Limiter back-end error — fail closed.
		code, msg := MapErr(ctx, h.deps.Logger, err)
		writeGraphqlBody(w, http.StatusInternalServerError, responseEnvelope{
			Errors: []Error{NewError(code, msg).withRequestID(middleware.RequestIDFrom(ctx))},
		})
		return
	}

	// 3. Pool selection (Reader vs Primary).
	doc, parseErr := cost.ParseQuery(env.Query)
	if parseErr != nil {
		// Should be unreachable — analyser would have caught it.
		code, msg := MapErr(ctx, h.deps.Logger, parseErr)
		writeGraphqlBody(w, http.StatusOK, responseEnvelope{
			Errors: []Error{NewError(code, msg).withRequestID(middleware.RequestIDFrom(ctx))},
		})
		return
	}
	op := doc.Operations[0]
	if env.OperationName != "" {
		for _, candidate := range doc.Operations {
			if candidate.Name == env.OperationName {
				op = candidate
				break
			}
		}
	}
	mds := middleware.SelectStore(h.deps.Pools, op)
	ctx = middleware.WithStore(ctx, mds)

	// 4. Execute.
	exec := &resolvers.Executor{Deps: h.deps.Resolvers, Vars: env.Variables}
	res := exec.Execute(ctx, doc, op)

	resp := responseEnvelope{
		Data: res.Data,
	}
	if len(res.Errors) > 0 {
		for _, ee := range res.Errors {
			code, msg := MapErr(ctx, h.deps.Logger, ee.Cause)
			gerr := Error{Message: msg, Path: ee.Path,
				Extensions: map[string]any{"code": string(code)}}
			resp.Errors = append(resp.Errors, gerr.withRequestID(middleware.RequestIDFrom(ctx)))
		}
	}
	resp.Extensions = map[string]any{
		"cost":  c.Complexity,
		"depth": c.Depth,
	}
	if persistedHit {
		resp.Extensions["persistedDiscount"] = h.deps.Analyzer.Limits.PersistedDiscount
	}
	if rid := middleware.RequestIDFrom(ctx); rid != "" {
		resp.Extensions["request_id"] = rid
	}
	writeGraphqlBody(w, http.StatusOK, resp)
}

// readRequest decodes a JSON envelope from a POST body.
func readRequest(r *http.Request) (requestEnvelope, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes))
	if err != nil {
		return requestEnvelope{}, err
	}
	var env requestEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return requestEnvelope{}, err
	}
	if env.Query == "" {
		return env, errors.New("query is required")
	}
	return env, nil
}

// writeGraphqlError writes a GraphQL-shaped error response with the
// supplied HTTP status. Used by paths (auth failure, body-cap) that fail
// before the executor runs.
func writeGraphqlError(w http.ResponseWriter, r *http.Request, status int, err Error) {
	rid := middleware.RequestIDFrom(r.Context())
	err = err.withRequestID(rid)
	writeGraphqlBody(w, status, responseEnvelope{Errors: []Error{err}})
}

func writeGraphqlBody(w http.ResponseWriter, status int, body responseEnvelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pkindToCost(k middleware.PrincipalKind) cost.PrincipalKind {
	switch k {
	case middleware.PrincipalHuman:
		return cost.PrincipalHuman
	case middleware.PrincipalAgent:
		return cost.PrincipalAgent
	default:
		return cost.PrincipalUnknown
	}
}

// statusForCode returns the conventional HTTP status for a GraphQL error.
// GraphQL responses normally always 200; we override for routes (persisted
// register / fetch) that semantically map to REST-shaped statuses.
func statusForCode(code ErrorCode) int {
	switch code {
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound, CodePersistedQueryNotFound:
		return http.StatusNotFound
	case CodeValidationFailed, CodePersistedQueryConflict:
		return http.StatusBadRequest
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
