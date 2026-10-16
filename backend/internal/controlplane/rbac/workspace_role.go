package rbac

// WorkspaceRole is a user's role in one workspace. It applies to every
// environment of that workspace.
type WorkspaceRole string

const (
	WorkspaceOwner  WorkspaceRole = "owner"
	WorkspaceAdmin  WorkspaceRole = "admin"
	WorkspaceEditor WorkspaceRole = "editor"
	WorkspaceViewer WorkspaceRole = "viewer"
)

var workspaceRank = map[WorkspaceRole]int{
	WorkspaceViewer: 1, WorkspaceEditor: 2, WorkspaceAdmin: 3, WorkspaceOwner: 4,
}

// ParseWorkspaceRole validates a role name from a request body.
func ParseWorkspaceRole(s string) (WorkspaceRole, bool) {
	r := WorkspaceRole(s)
	_, ok := workspaceRank[r]
	return r, ok
}

// AtLeast reports whether r grants everything min does.
func (r WorkspaceRole) AtLeast(min WorkspaceRole) bool {
	return workspaceRank[r] >= workspaceRank[min] && workspaceRank[r] > 0
}

// LowerWorkspaceRole is the lesser of two roles: what an API token may do is the
// lower of its own ceiling and its creator's current role.
func LowerWorkspaceRole(a, b WorkspaceRole) WorkspaceRole {
	if workspaceRank[b] < workspaceRank[a] || workspaceRank[a] == 0 {
		return b
	}
	return a
}

// EnvRole is the access the role gives inside each environment. Owners and
// admins can do everything flags and experiments allow, including changing
// production (the "approver" level); editors can edit outside production and
// kill anywhere; viewers read.
func (r WorkspaceRole) EnvRole() Role {
	switch r {
	case WorkspaceOwner, WorkspaceAdmin:
		return Admin
	case WorkspaceEditor:
		return Editor
	default:
		return Viewer
	}
}
