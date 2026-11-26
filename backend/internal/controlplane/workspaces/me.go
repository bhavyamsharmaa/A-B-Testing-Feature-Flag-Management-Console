package workspaces

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

type Handlers struct {
	svc   *Service
	email *auth.EmailPolicy
	// invitesByID: whether an invite can be accepted by its id (and open
	// invites are listed in /me). Off, only the one-time link works, which
	// proves the person was handed the invite and does not rely on the identity
	// provider having verified their email address.
	invitesByID bool
}

// NewHandlers wires the workspace routes. email decides whether a user whose
// address is not confirmed may bootstrap a workspace, create one, or accept an
// invite (nil means no check).
func NewHandlers(svc *Service, email *auth.EmailPolicy, invitesByID bool) *Handlers {
	return &Handlers{svc: svc, email: email, invitesByID: invitesByID}
}

// requireConfirmedEmail answers 403 EMAIL_NOT_CONFIRMED (the console shows a
// "please confirm your email" screen for it) or 503 when the status can't be
// established, and reports whether the request may go on.
func (h *Handlers) requireConfirmedEmail(w http.ResponseWriter, r *http.Request, user auth.User) bool {
	switch err := h.email.Require(r.Context(), user); {
	case err == nil:
		return true
	case errors.Is(err, auth.ErrEmailStatusUnavailable):
		httpx.WriteError(w, http.StatusServiceUnavailable, "EMAIL_STATUS_UNAVAILABLE", "could not check your email confirmation right now; try again in a moment")
	default:
		httpx.WriteError(w, http.StatusForbidden, "EMAIL_NOT_CONFIRMED", "Please confirm your email first: open the link we sent you, then continue.")
	}
	return false
}

type environmentView struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	IsProduction bool   `json:"isProduction"`
}

type workspaceView struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Slug         string             `json:"slug"`
	Role         rbac.WorkspaceRole `json:"role"`
	Environments []environmentView  `json:"environments"`
}

type inviteForMeView struct {
	ID             string             `json:"id"`
	WorkspaceID    string             `json:"workspaceId"`
	WorkspaceName  string             `json:"workspaceName"`
	Role           rbac.WorkspaceRole `json:"role"`
	InvitedByEmail string             `json:"invitedByEmail"`
	ExpiresAt      time.Time          `json:"expiresAt"`
}

type meResponse struct {
	ID                string            `json:"id"`
	Email             string            `json:"email"`
	ActiveWorkspaceID string            `json:"activeWorkspaceId"`
	Workspaces        []workspaceView   `json:"workspaces"`
	Invites           []inviteForMeView `json:"invites"`
}

func viewOf(w Workspace) workspaceView {
	v := workspaceView{ID: w.ID, Name: w.Name, Slug: w.Slug, Role: w.Role, Environments: make([]environmentView, 0, len(w.Environments))}
	for _, e := range w.Environments {
		v.Environments = append(v.Environments, environmentView{ID: e.ID, Key: e.Key, Name: e.Name, IsProduction: e.IsProduction})
	}
	return v
}

func viewsOf(list []Workspace) []workspaceView {
	out := make([]workspaceView, 0, len(list))
	for _, w := range list {
		out = append(out, viewOf(w))
	}
	return out
}

func buildMe(user auth.User, list []Workspace, invites []PendingInvite) meResponse {
	resp := meResponse{ID: user.ID, Email: user.Email, Workspaces: viewsOf(list), Invites: make([]inviteForMeView, 0, len(invites))}
	if len(list) > 0 {
		resp.ActiveWorkspaceID = list[0].ID // load orders the most recently used first
	}
	for _, i := range invites {
		resp.Invites = append(resp.Invites, inviteForMeView{
			ID: i.ID, WorkspaceID: i.WorkspaceID, WorkspaceName: i.WorkspaceName, Role: i.Role,
			InvitedByEmail: i.InvitedByEmail, ExpiresAt: i.ExpiresAt,
		})
	}
	return resp
}

// Me handles GET /me: the caller, every workspace they belong to (with their
// role and its environments), the active one, and open invites addressed to
// their email. A user's first call creates their personal workspace.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if !h.requireConfirmedEmail(w, r, user) { // before anything is created
		return
	}
	list, err := h.svc.Ensure(r.Context(), user)
	if err != nil {
		httpx.WriteInternal(w, r, err)
		return
	}
	invites := []PendingInvite{}
	if h.invitesByID {
		var err error
		if invites, err = pendingInvites(r.Context(), h.svc.pool, user.Email); err != nil {
			httpx.WriteInternal(w, r, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, buildMe(user, list, invites))
}

// lower-cases a path id so it compares equal to the ids we store.
func pathID(r *http.Request, name string) string { return strings.ToLower(r.PathValue(name)) }
