package restapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/google/uuid"
)

type reposHandlers struct {
	svc    repositories.Service
	logger *slog.Logger
}

type repositoryResponse struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	OwnerID       string    `json:"owner_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	DefaultBranch string    `json:"default_branch"`
	Visibility    string    `json:"visibility"`
	CreatedAt     time.Time `json:"created_at"`
}

func toRepositoryResponse(r *repositories.Repository) repositoryResponse {
	return repositoryResponse{
		ID:            r.ID.String(),
		OrgID:         r.OrgID.String(),
		OwnerID:       r.OwnerID.String(),
		Slug:          r.Slug,
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		Visibility:    r.Visibility,
		CreatedAt:     r.CreatedAt,
	}
}

type listResponse struct {
	Items      []repositoryResponse `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// createRepo: humans create repositories under an org they belong to. Agent
// creation paths land via the MCP server (issue #112); they are explicitly
// rejected here so we don't accidentally provide an unmetered REST escape
// hatch for agents.
func (h *reposHandlers) createRepo(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "missing principal")
		return
	}
	if p.Kind() != PrincipalHuman {
		writeError(w, r, http.StatusForbidden, CodeForbidden, "only humans may create repositories via REST")
		return
	}
	var body struct {
		OrgID         string `json:"org_id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		DefaultBranch string `json:"default_branch"`
		Visibility    string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "invalid JSON body")
		return
	}
	orgID, err := uuid.Parse(body.OrgID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "org_id is not a UUID")
		return
	}
	repo, err := h.svc.CreateRepository(r.Context(), repositories.CreateInput{
		OrgID:         orgID,
		OwnerID:       p.ID(),
		Slug:          body.Slug,
		Name:          body.Name,
		DefaultBranch: body.DefaultBranch,
		Visibility:    body.Visibility,
	})
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	w.Header().Set("Location", "/v1/repos/"+repo.ID.String())
	writeJSON(w, http.StatusCreated, toRepositoryResponse(repo))
}

func (h *reposHandlers) getRepo(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	repo, err := h.svc.GetRepository(r.Context(), id)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	if repo == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "repository not found")
		return
	}
	writeJSON(w, http.StatusOK, toRepositoryResponse(repo))
}

// deleteRepo reserves the URL space for repository deletion. The actual
// delete pipeline (cascade across collaboration tables, schedule git-plane
// deprovision) is tracked under a follow-up issue; until then this returns
// 501 with the documented internal-class code.
func (h *reposHandlers) deleteRepo(w http.ResponseWriter, r *http.Request) {
	if _, err := uuid.Parse(r.PathValue("id")); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "id is not a UUID")
		return
	}
	if h.logger != nil {
		h.logger.WarnContext(r.Context(), "rest_api: DELETE /v1/repos is not implemented; tracked as follow-up")
	}
	writeError(w, r, http.StatusNotImplemented, CodeInternal, "repository deletion not implemented")
}

func (h *reposHandlers) listOrgRepos(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "org is not a UUID")
		return
	}
	cursor, err := DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "invalid cursor")
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n <= 0 {
			writeError(w, r, http.StatusBadRequest, CodeValidationFailed, "limit must be a positive integer")
			return
		}
		limit = n
	}
	rows, next, err := h.svc.ListByOrg(r.Context(), orgID, cursor, limit)
	if err != nil {
		status, code, msg := mapErr(r.Context(), h.logger, err)
		writeError(w, r, status, code, msg)
		return
	}
	resp := listResponse{Items: make([]repositoryResponse, 0, len(rows))}
	for i := range rows {
		resp.Items = append(resp.Items, toRepositoryResponse(&rows[i]))
	}
	if next != nil {
		resp.NextCursor = EncodeCursor(*next)
	}
	writeJSON(w, http.StatusOK, resp)
}

