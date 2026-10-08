package workspaces

import (
	"net/http"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

type workspaceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type environmentView struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	IsProduction bool      `json:"isProduction"`
	Role         rbac.Role `json:"role"`
}

// legacyRole is the pre-workspace shape of /me, kept so the deployed console
// (which reads `roles`) keeps working until it moves to `environments`.
// TODO(stage-2): remove with the environment-key shim.
type legacyRole struct {
	Environment string    `json:"environment"`
	Role        rbac.Role `json:"role"`
}

type meResponse struct {
	ID           string            `json:"id"`
	Email        string            `json:"email"`
	Workspace    workspaceView     `json:"workspace"`
	Environments []environmentView `json:"environments"`
	Roles        []legacyRole      `json:"roles"`
}

func buildMe(user auth.User, ws Workspace) meResponse {
	resp := meResponse{
		ID:           user.ID,
		Email:        user.Email,
		Workspace:    workspaceView{ID: ws.ID, Name: ws.Name},
		Environments: make([]environmentView, 0, len(ws.Environments)),
		Roles:        make([]legacyRole, 0, len(ws.Environments)),
	}
	for _, e := range ws.Environments {
		resp.Environments = append(resp.Environments, environmentView{ID: e.ID, Key: e.Key, Name: e.Name, IsProduction: e.IsProduction, Role: e.Role})
		resp.Roles = append(resp.Roles, legacyRole{Environment: e.Key, Role: e.Role})
	}
	return resp
}

// Me handles GET /me: the caller, their workspace and their role in each of
// its environments. A user's first call creates the workspace.
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	ws, err := s.Ensure(r.Context(), user)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, buildMe(user, ws))
}
