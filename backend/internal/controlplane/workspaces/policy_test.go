package workspaces

import (
	"strings"
	"testing"

	"helios/backend/internal/controlplane/rbac"
)

const (
	owner  = rbac.WorkspaceOwner
	admin  = rbac.WorkspaceAdmin
	editor = rbac.WorkspaceEditor
	viewer = rbac.WorkspaceViewer
)

func TestCanChangeRole(t *testing.T) {
	cases := []struct {
		name                string
		actor, current, nxt rbac.WorkspaceRole
		ok                  bool
	}{
		{"owner promotes viewer to admin", owner, viewer, admin, true},
		{"owner hands over ownership", owner, admin, owner, true},
		{"owner demotes another owner", owner, owner, admin, true},
		{"admin promotes viewer to editor", admin, viewer, editor, true},
		{"admin demotes another admin", admin, admin, editor, true},
		{"admin cannot create an owner", admin, editor, owner, false},
		{"admin cannot touch an owner", admin, owner, admin, false},
		{"editor cannot change roles", editor, viewer, editor, false},
		{"viewer cannot change roles", viewer, viewer, viewer, false},
	}
	for _, c := range cases {
		err := canChangeRole(c.actor, c.current, c.nxt)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func TestCanRemove(t *testing.T) {
	cases := []struct {
		name          string
		actor, target rbac.WorkspaceRole
		self, ok      bool
	}{
		{"viewer leaves", viewer, viewer, true, true},
		{"owner leaves", owner, owner, true, true},
		{"admin removes editor", admin, editor, false, true},
		{"admin removes another admin", admin, admin, false, true},
		{"admin cannot remove owner", admin, owner, false, false},
		{"owner removes owner", owner, owner, false, true},
		{"editor cannot remove viewer", editor, viewer, false, false},
		{"viewer cannot remove viewer", viewer, viewer, false, false},
	}
	for _, c := range cases {
		err := canRemove(c.actor, c.target, c.self)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func TestCanInvite(t *testing.T) {
	for _, a := range []rbac.WorkspaceRole{owner, admin} {
		for _, r := range []rbac.WorkspaceRole{admin, editor, viewer} {
			if err := canInvite(a, r); err != nil {
				t.Errorf("%s inviting %s: %v", a, r, err)
			}
		}
		if err := canInvite(a, owner); err == nil {
			t.Errorf("%s inviting an owner was allowed", a)
		}
	}
	for _, a := range []rbac.WorkspaceRole{editor, viewer} {
		if err := canInvite(a, viewer); err == nil {
			t.Errorf("%s can invite", a)
		}
	}
}

func TestWouldLeaveNoOwner(t *testing.T) {
	cases := []struct {
		name          string
		owners        int
		current, next rbac.WorkspaceRole
		want          bool
	}{
		{"last owner removed", 1, owner, "", true},
		{"last owner demoted", 1, owner, admin, true},
		{"one of two owners removed", 2, owner, "", false},
		{"owner stays owner", 1, owner, owner, false},
		{"admin removed", 1, admin, "", false},
		{"no owners at all (legacy)", 0, admin, "", false},
	}
	for _, c := range cases {
		if got := wouldLeaveNoOwner(c.owners, c.current, c.next); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestCleanName(t *testing.T) {
	if got, err := cleanName("  Acme Inc  "); err != nil || got != "Acme Inc" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 61), "bad\x00name", "tab\tname"} {
		if _, err := cleanName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := cleanName(strings.Repeat("é", 60)); err != nil {
		t.Errorf("60 multi-byte characters rejected: %v", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, ok := normalizeEmail("  Alice@Example.COM "); !ok || got != "alice@example.com" {
		t.Errorf("got %q %v", got, ok)
	}
	for _, bad := range []string{"", "a", "a@b", "@example.com", "a@@example.com", "a b@example.com", "a@example.", "a@.com", "a@example.com,b@example.com", "<a@example.com>"} {
		if _, ok := normalizeEmail(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Inc":              "acme-inc",
		"alice's workspace":     "alice-s-workspace",
		"  --Hello__World!! ":   "hello-world",
		"":                      "workspace",
		"日本語":                   "workspace",
		strings.Repeat("a", 50): strings.Repeat("a", 30),
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	s, err := newSlug("Acme Inc")
	if err != nil || !strings.HasPrefix(s, "acme-inc-") || len(s) != len("acme-inc-")+6 {
		t.Errorf("newSlug = %q, %v", s, err)
	}
	s2, _ := newSlug("Acme Inc")
	if s == s2 {
		t.Error("two slugs for the same name collided")
	}
}

func TestInviteToken(t *testing.T) {
	tok, hash, err := newInviteToken()
	if err != nil || !strings.HasPrefix(tok, "hinv_") || len(tok) < 40 {
		t.Fatalf("token %q, %v", tok, err)
	}
	if hash != hashToken(tok) || strings.Contains(hash, tok) || len(hash) != 64 {
		t.Errorf("hash %q", hash)
	}
	tok2, hash2, _ := newInviteToken()
	if tok == tok2 || hash == hash2 {
		t.Error("tokens repeat")
	}
}
