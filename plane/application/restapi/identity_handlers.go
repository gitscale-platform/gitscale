package restapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/identity"
	"github.com/google/uuid"
)

// identityHandlers wraps identity.Service. All authorisation rules live here;
// the underlying service is unauthenticated by design (called from gRPC,
// REST, MCP, etc).
type identityHandlers struct {
	svc    identity.Service
	logger *slog.Logger
}

// userResponse is the JSON shape returned for /v1/users/* endpoints. It is
// distinct from store.HumanUser so the credential hash never leaks.
type userResponse struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	RateBucket string    `json:"rate_bucket"`
	CreatedAt  time.Time `json:"created_at"`
}

func toUserResponse(u *identity.HumanUser) userResponse {
	return userResponse{
		ID:         u.ID.String(),
		Email:      u.Email,
		RateBucket: u.RateBucket,
		CreatedAt:  u.CreatedAt,
	}
}

type agentResponse struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"display_name"`
	ParentUserID    string    `json:"parent_user_id"`
	PermissionScope []string  `json:"permission_scope"`
	RateBucket      string    `json:"rate_bucket"`
	ReputationScore float64   `json:"reputation_score"`
	CreatedAt       time.Time `json:"created_at"`
}

func toAgentResponse(a *identity.AgentIdentity) agentResponse {
	scope := append([]string{}, a.PermissionScope...)
	return agentResponse{
		ID:              a.ID.String(),
		DisplayName:     a.DisplayName,
		ParentUserID:    a.ParentUserID.String(),
		PermissionScope: scope,
		RateBucket:      a.RateBucket,
		ReputationScore: a.ReputationScore,
		CreatedAt:       a.CreatedAt,
	}
}

func (h *identityHandlers) createUser(w http.ResponseWriter, r *http.Request) {
	// Authorisation: only humans may create new humans. Agent self-signup
	// is forbidden — agents must be derived from an existing human.
	if p := PrincipalFromContext(r.Context()); p != nil && p.Kind() == PrincipalAgent {
		writeError(w, r, http.StatusForbidden, CodeForbidden, "agents may not create users")
		return
	}
	var body struct {
		Email      string `json:"email"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "invalid JSON body")
		return
	}
	u, err := h.svc.CreateUser(r.Context(), body.Email, body.Credential)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	w.Header().Set("Location", "/v1/users/"+u.ID.String())
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

func (h *identityHandlers) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	u, err := h.svc.GetUser(r.Context(), id)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	if u == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (h *identityHandlers) createAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentUserID    string   `json:"parent_user_id"`
		DisplayName     string   `json:"display_name"`
		PermissionScope []string `json:"permission_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "invalid JSON body")
		return
	}
	parent, err := uuid.Parse(body.ParentUserID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "parent_user_id is not a UUID")
		return
	}
	// Authorisation: only the parent human (or an admin in a later iteration)
	// may create agents under itself. An agent caller may not create siblings.
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing principal")
		return
	}
	if p.Kind() != PrincipalHuman || p.ID() != parent {
		writeError(w, r, http.StatusForbidden, CodeForbidden, "principal may not create agents under another user")
		return
	}
	a, err := h.svc.CreateAgent(r.Context(), parent, body.DisplayName, body.PermissionScope)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	w.Header().Set("Location", "/v1/agents/"+a.ID.String())
	writeJSON(w, http.StatusCreated, toAgentResponse(a))
}

func (h *identityHandlers) getAgent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	a, err := h.svc.GetAgent(r.Context(), id)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	if a == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, toAgentResponse(a))
}

func (h *identityHandlers) revokeAgent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	// Empty body is acceptable — the reason is optional.
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Authorisation: a human may revoke agents under themselves; an agent
	// may revoke only itself. Cross-user revocation is forbidden in this
	// iteration (admin scopes are tracked under issue #15-revocation).
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing principal")
		return
	}
	target, err := h.svc.GetAgent(r.Context(), id)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	if target == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "agent not found")
		return
	}
	switch p.Kind() {
	case PrincipalHuman:
		if p.ID() != target.ParentUserID {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "human may only revoke agents they own")
			return
		}
	case PrincipalAgent:
		if p.ID() != target.ID {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "agent may only revoke itself")
			return
		}
	default:
		writeError(w, r, http.StatusForbidden, CodeForbidden, "unknown principal kind")
		return
	}

	if err := h.svc.RevokeAgent(r.Context(), id, body.Reason); err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *identityHandlers) updatePerms(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	var body struct {
		PermissionScope []string `json:"permission_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "invalid JSON body")
		return
	}
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing principal")
		return
	}
	target, err := h.svc.GetAgent(r.Context(), id)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	if target == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "agent not found")
		return
	}
	// Only the parent human may change scope. Agents cannot self-elevate.
	if p.Kind() != PrincipalHuman || p.ID() != target.ParentUserID {
		writeError(w, r, http.StatusForbidden, CodeForbidden, "only the parent user may update agent permissions")
		return
	}

	if err := h.svc.UpdateAgentPermissions(r.Context(), id, body.PermissionScope); err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
