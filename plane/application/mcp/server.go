package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/restapi"
	"github.com/gitscale-platform/gitscale/plane/data/ratelimit"
)

// SessionTTL is the lifetime of a session token returned by initialize.
// Short enough that a leaked token's blast radius is bounded; long
// enough for an interactive agent session.
const SessionTTL = 30 * time.Minute

// Server is the MCP HTTP handler. Construct via NewServer; treat the
// zero value as invalid.
type Server struct {
	cfg      Config
	deps     Deps
	registry *Registry
	logger   *slog.Logger
	now      func() time.Time
}

// NewServer wires the MCP HTTP handler. Returns a non-nil error if a
// required dependency is nil or Config is invalid.
//
// At construction we WARN-log when ProtocolVersion is the deferred
// default (per spec; the policy ADR is open until July 2026).
func NewServer(cfg Config, d Deps) (*Server, error) {
	if cfg.ProtocolVersion == "" {
		return nil, errors.New("mcp: Config.ProtocolVersion is required")
	}
	if len(cfg.SessionHMACSecret) < MinSessionHMACSecretBytes {
		return nil, fmt.Errorf("mcp: Config.SessionHMACSecret must be at least %d bytes", MinSessionHMACSecretBytes)
	}
	if d.Resolver == nil {
		return nil, errors.New("mcp: Deps.Resolver is required")
	}
	if d.Limiter == nil {
		return nil, errors.New("mcp: Deps.Limiter is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ProtocolVersion == DeferredDefaultProtocolVersion {
		logger.Warn("mcp.protocol_version_deferred",
			slog.String("version", cfg.ProtocolVersion),
			slog.String("note", "MCP protocol-version policy ADR is open (target July 2026); see CLAUDE.md"))
	}
	reg := NewRegistry()
	RegisterDefaults(reg, d)
	return &Server{
		cfg:      cfg,
		deps:     d,
		registry: reg,
		logger:   logger,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
}

// Handler returns an http.Handler that implements the MCP HTTP+JSON
// transport. The middleware order mirrors the REST router: RequestID
// → Auth → RateLimit(SurfaceMCP) → MCP dispatch. Reusing those
// middlewares means observability + auth semantics live in one place.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp/v1/initialize", s.handleInitialize)
	mux.HandleFunc("POST /mcp/v1/tools/list", s.handleToolsList)
	mux.HandleFunc("POST /mcp/v1/tools/call", s.handleToolsCall)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	var handler http.Handler = mux
	handler = mcpRateLimit(s.deps.Limiter, s.cfg.RateConfig)(handler)
	handler = restapi.AuthMiddlewareForMCP(s.deps.Resolver)(handler)
	handler = restapi.RequestIDMiddleware()(handler)
	return handler
}

// ─── JSON-RPC envelope helpers ───────────────────────────────────────────────

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func decodeRequest(r *http.Request) (jsonRPCRequest, error) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, err
	}
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	return req, nil
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code Code, message string, requestID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatusForCode(code))
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	}
	if requestID != "" {
		resp.Error.Data = map[string]string{"request_id": requestID}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// httpStatusForCode picks an HTTP status that matches the JSON-RPC
// code's semantics. JSON-RPC clients ignore the status — the body is
// load-bearing — but the status helps debuggers see auth/rate-limit
// outcomes without parsing the body.
func httpStatusForCode(c Code) int {
	switch c {
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeNotImplemented:
		return http.StatusNotImplemented
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeInvalidParams, CodeMethodNotFound:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// ─── handlers ───────────────────────────────────────────────────────────────

// InitializeResult is the body returned by /mcp/v1/initialize.
type InitializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ServerInfo      InitializeServerInfo   `json:"serverInfo"`
	SessionID       string                 `json:"sessionId"`
	ExpiresAt       string                 `json:"expiresAt"`
	Meta            map[string]any         `json:"_meta,omitempty"`
}

// InitializeServerInfo identifies the server to the client.
type InitializeServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(r)
	if err != nil {
		writeRPCError(w, nil, CodeInvalidParams, "malformed JSON-RPC request", restapi.RequestIDFromContext(r.Context()))
		return
	}
	p := restapi.PrincipalFromContext(r.Context())
	if p == nil {
		writeRPCError(w, req.ID, CodeUnauthenticated, "missing principal", restapi.RequestIDFromContext(r.Context()))
		return
	}
	expiresAt := s.now().Add(SessionTTL)
	tok, err := MintSession(s.cfg.SessionHMACSecret, Session{
		PrincipalID:     p.ID(),
		ProtocolVersion: s.cfg.ProtocolVersion,
		ExpiresAt:       expiresAt,
	})
	if err != nil {
		s.logger.ErrorContext(r.Context(), "mcp.session_mint_failed", slog.String("err", err.Error()))
		writeRPCError(w, req.ID, CodeInternal, "session mint failed", restapi.RequestIDFromContext(r.Context()))
		return
	}
	result := InitializeResult{
		ProtocolVersion: s.cfg.ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		ServerInfo: InitializeServerInfo{
			Name:    s.cfg.ServerName,
			Version: s.cfg.ServerVersion,
		},
		SessionID: tok,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}
	if s.cfg.ProtocolVersion == DeferredDefaultProtocolVersion {
		result.Meta = map[string]any{
			"protocol_version_deferred": true,
			"note":                      "MCP protocol-version policy ADR is open (target July 2026)",
		}
	}
	writeResult(w, req.ID, result)
}

// ToolsListResult is the body returned by /mcp/v1/tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(r)
	if err != nil {
		writeRPCError(w, nil, CodeInvalidParams, "malformed JSON-RPC request", restapi.RequestIDFromContext(r.Context()))
		return
	}
	if restapi.PrincipalFromContext(r.Context()) == nil {
		writeRPCError(w, req.ID, CodeUnauthenticated, "missing principal", restapi.RequestIDFromContext(r.Context()))
		return
	}
	writeResult(w, req.ID, ToolsListResult{Tools: s.registry.Manifest()})
}

// ToolsCallParams is the body of /mcp/v1/tools/call's `params`.
type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	req, err := decodeRequest(r)
	if err != nil {
		writeRPCError(w, nil, CodeInvalidParams, "malformed JSON-RPC request", restapi.RequestIDFromContext(r.Context()))
		return
	}
	p := restapi.PrincipalFromContext(r.Context())
	if p == nil {
		writeRPCError(w, req.ID, CodeUnauthenticated, "missing principal", restapi.RequestIDFromContext(r.Context()))
		return
	}
	var params ToolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, CodeInvalidParams, "params: "+err.Error(), restapi.RequestIDFromContext(r.Context()))
		return
	}
	tool, ok := s.registry.Get(params.Name)
	if !ok {
		writeRPCError(w, req.ID, CodeMethodNotFound, fmt.Sprintf("unknown tool %q", params.Name), restapi.RequestIDFromContext(r.Context()))
		return
	}
	result, err := tool.Handler(r.Context(), p, params.Arguments)
	if err != nil {
		code, msg := mapErr(r.Context(), err)
		// Log internal errors at error level so they show up in
		// observability without leaking the underlying message to the
		// client (we send mapErr's stable message instead).
		if code == CodeInternal {
			s.logger.ErrorContext(r.Context(), "mcp.tool_internal_error",
				slog.String("tool", params.Name),
				slog.String("err", err.Error()))
		}
		writeRPCError(w, req.ID, code, msg, restapi.RequestIDFromContext(r.Context()))
		return
	}
	writeResult(w, req.ID, result)
}

// ─── rate-limit middleware (MCP surface) ─────────────────────────────────────

func mcpRateLimit(limiter ratelimit.RateLimiter, cfg restapi.RateConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			p := restapi.PrincipalFromContext(r.Context())
			if p == nil {
				// Auth must always run before rate-limit. Defensive
				// guard mirrors the REST middleware.
				writeRPCError(w, nil, CodeUnauthenticated, "missing principal", restapi.RequestIDFromContext(r.Context()))
				return
			}
			capacity, refill := bucketParamsForKind(cfg, p.Kind())
			if capacity <= 0 || refill < 0 {
				next.ServeHTTP(w, r)
				return
			}
			key := fmt.Sprintf(ratelimit.TokenBucketKey, p.ID(), ratelimit.SurfaceMCP)
			granted, _, err := limiter.Take(r.Context(), key, capacity, refill, 1)
			if err != nil {
				writeRPCError(w, nil, CodeInternal, "rate-limit backend error", restapi.RequestIDFromContext(r.Context()))
				return
			}
			if !granted {
				writeRPCError(w, nil, CodeRateLimited, "rate limit exceeded", restapi.RequestIDFromContext(r.Context()))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bucketParamsForKind(cfg restapi.RateConfig, kind restapi.PrincipalKind) (capacity, refill float64) {
	switch kind {
	case restapi.PrincipalAgent:
		return cfg.AgentCapacity, cfg.AgentRefillPerSec
	case restapi.PrincipalHuman:
		return cfg.HumanCapacity, cfg.HumanRefillPerSec
	default:
		return 0, 0
	}
}

// ctxFromRequest is unused but kept for documentation purposes; the
// MCP server does not augment the request context beyond what the
// REST middlewares produce.
var _ context.Context = context.Background()
