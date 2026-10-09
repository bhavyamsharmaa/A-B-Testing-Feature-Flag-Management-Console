package workspaces

import (
	"net/http"
	"time"

	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
	"helios/backend/internal/platform/httpx"
)

type nameRequest struct {
	Name string `json:"name"`
}

// List handles GET /workspaces: the caller's workspaces (same data as /me).
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	list, err := h.svc.Ensure(r.Context(), user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workspaces": viewsOf(list)})
}

// Create handles POST /workspaces: another workspace, owned by the caller,
// which becomes their active one.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	ws, err := h.svc.Create(r.Context(), user, req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, viewOf(ws))
}

// Rename handles PATCH /workspaces/{wsId} (admin+).
func (h *Handlers) Rename(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	var req nameRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	name, err := h.svc.Rename(r.Context(), access, req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": access.WorkspaceID, "name": name})
}

// Switch handles POST /workspaces/{wsId}/switch: remember it as the caller's
// active workspace. Only members can; anyone else gets the guard's 404.
func (h *Handlers) Switch(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	if err := h.svc.Switch(r.Context(), access); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"activeWorkspaceId": access.WorkspaceID})
}

type memberView struct {
	UserID   string             `json:"userId"`
	Email    string             `json:"email"`
	Role     rbac.WorkspaceRole `json:"role"`
	JoinedAt time.Time          `json:"joinedAt"`
	IsYou    bool               `json:"isYou"`
}

// Members handles GET /workspaces/{wsId}/members (any member).
func (h *Handlers) Members(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	list, err := h.svc.Members(r.Context(), access.WorkspaceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]memberView, 0, len(list))
	for _, m := range list {
		out = append(out, memberView{UserID: m.UserID, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt, IsYou: m.UserID == access.User.ID})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"members": out})
}

type roleRequest struct {
	Role string `json:"role"`
}

// ChangeRole handles PATCH /workspaces/{wsId}/members/{userId} (admin+).
func (h *Handlers) ChangeRole(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	target := pathID(r, "userId")
	var req roleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	role, ok := rbac.ParseWorkspaceRole(req.Role)
	if !ok {
		httpx.BadRequest(w, "role must be one of owner, admin, editor, viewer")
		return
	}
	if !rbac.IsUUID(target) {
		writeError(w, r, errMemberNotFound)
		return
	}
	if err := h.svc.ChangeRole(r.Context(), access, target, role); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"userId": target, "role": role})
}

// RemoveMember handles DELETE /workspaces/{wsId}/members/{userId}. Admins
// remove others; anyone may remove themselves (leave the workspace).
func (h *Handlers) RemoveMember(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	target := pathID(r, "userId")
	if !rbac.IsUUID(target) {
		writeError(w, r, errMemberNotFound)
		return
	}
	if err := h.svc.RemoveMember(r.Context(), access, target); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type inviteView struct {
	ID             string             `json:"id"`
	Email          string             `json:"email"`
	Role           rbac.WorkspaceRole `json:"role"`
	InvitedByEmail string             `json:"invitedByEmail"`
	CreatedAt      time.Time          `json:"createdAt"`
	ExpiresAt      time.Time          `json:"expiresAt"`
}

func inviteOf(i Invite) inviteView {
	return inviteView{ID: i.ID, Email: i.Email, Role: i.Role, InvitedByEmail: i.InvitedByEmail, CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt}
}

// Invites handles GET /workspaces/{wsId}/invites: open invites (admin+).
func (h *Handlers) Invites(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	list, err := h.svc.Invites(r.Context(), access.WorkspaceID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]inviteView, 0, len(list))
	for _, i := range list {
		out = append(out, inviteOf(i))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"invites": out})
}

type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// CreateInvite handles POST /workspaces/{wsId}/invites (admin+). The token in
// the response is the only time it is shown; the console turns it into a
// link. Helios does not send email.
func (h *Handlers) CreateInvite(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	var req createInviteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	role, ok := rbac.ParseWorkspaceRole(req.Role)
	if !ok {
		httpx.BadRequest(w, "role must be one of admin, editor, viewer")
		return
	}
	inv, token, err := h.svc.CreateInvite(r.Context(), access, req.Email, role)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"invite": inviteOf(inv), "token": token})
}

// RevokeInvite handles DELETE /workspaces/{wsId}/invites/{inviteId} (admin+).
func (h *Handlers) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	access := rbac.WorkspaceAccessFrom(r.Context())
	if err := h.svc.RevokeInvite(r.Context(), access, pathID(r, "inviteId")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type acceptRequest struct {
	Token    string `json:"token"`
	InviteID string `json:"inviteId"`
}

// AcceptInvite handles POST /invites/accept with the one-time token, or the
// id of an invite listed in /me. The caller's email must match the invite.
func (h *Handlers) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	var req acceptRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	if (req.Token == "") == (req.InviteID == "") {
		httpx.BadRequest(w, "provide exactly one of token or inviteId")
		return
	}
	wsID, err := h.svc.AcceptInvite(r.Context(), user, req.Token, req.InviteID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"workspaceId": wsID})
}
