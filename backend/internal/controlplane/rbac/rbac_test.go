package rbac

import "testing"

// The RBAC matrix from the PRD (US-06), expressed as the requirement each
// route uses in cmd/api.
func TestMatrix(t *testing.T) {
	dev := Environment{Key: "dev"}
	staging := Environment{Key: "staging"}
	prod := Environment{Key: "production", IsProduction: true}

	type action struct {
		name string
		req  Requirement
	}
	read := action{"read flags", Min(Viewer)}
	create := action{"create flag", Min(Editor)}
	edit := action{"edit/toggle flag", FlagWrite}
	kill := action{"kill switch", Min(Editor)}
	members := action{"manage members", Min(Admin)}

	allowed := map[Role]map[string][]Environment{
		Viewer:   {read.name: {dev, staging, prod}},
		Editor:   {read.name: {dev, staging, prod}, create.name: {dev, staging, prod}, edit.name: {dev, staging}, kill.name: {dev, staging, prod}},
		Approver: {read.name: {dev, staging, prod}, create.name: {dev, staging, prod}, edit.name: {dev, staging, prod}, kill.name: {dev, staging, prod}},
		Admin:    {read.name: {dev, staging, prod}, create.name: {dev, staging, prod}, edit.name: {dev, staging, prod}, kill.name: {dev, staging, prod}, members.name: {dev, staging, prod}},
	}

	for role, grants := range allowed {
		for _, a := range []action{read, create, edit, kill, members} {
			for _, env := range []Environment{dev, staging, prod} {
				want := false
				for _, e := range grants[a.name] {
					if e.Key == env.Key {
						want = true
					}
				}
				if got := role.AtLeast(a.req(env)); got != want {
					t.Errorf("%s / %s / %s: allowed=%v, want %v", role, a.name, env.Key, got, want)
				}
			}
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, s := range []string{"viewer", "editor", "approver", "admin"} {
		if _, ok := ParseRole(s); !ok {
			t.Errorf("ParseRole(%q) rejected", s)
		}
	}
	for _, s := range []string{"", "Admin", "owner"} {
		if _, ok := ParseRole(s); ok {
			t.Errorf("ParseRole(%q) accepted", s)
		}
	}
}
