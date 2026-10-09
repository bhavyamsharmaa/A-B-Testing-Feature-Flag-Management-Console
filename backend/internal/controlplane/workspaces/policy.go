package workspaces

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"helios/backend/internal/controlplane/rbac"
)

// roleError is a refusal the API reports as 403 FORBIDDEN with this message.
type roleError struct{ msg string }

func (e roleError) Error() string { return e.msg }

// canChangeRole decides whether actor may move a member from current to next.
// Owners can do anything, including handing ownership over. Admins manage
// everyone who is not an owner and can't create owners.
func canChangeRole(actor, current, next rbac.WorkspaceRole) error {
	if !actor.AtLeast(rbac.WorkspaceAdmin) {
		return roleError{"only owners and admins can change roles"}
	}
	if actor != rbac.WorkspaceOwner && (current == rbac.WorkspaceOwner || next == rbac.WorkspaceOwner) {
		return roleError{"only an owner can change an owner's role or make someone an owner"}
	}
	return nil
}

// canRemove decides whether actor may remove target from the workspace.
// Anyone may remove themselves (leave); removing someone else needs admin,
// and an owner can only be removed by an owner.
func canRemove(actor, target rbac.WorkspaceRole, self bool) error {
	if self {
		return nil
	}
	if !actor.AtLeast(rbac.WorkspaceAdmin) {
		return roleError{"only owners and admins can remove members"}
	}
	if target == rbac.WorkspaceOwner && actor != rbac.WorkspaceOwner {
		return roleError{"only an owner can remove an owner"}
	}
	return nil
}

// canInvite: owners and admins may invite, never as owner (ownership is
// handed over by changing a member's role).
func canInvite(actor, role rbac.WorkspaceRole) error {
	if !actor.AtLeast(rbac.WorkspaceAdmin) {
		return roleError{"only owners and admins can invite people"}
	}
	if role == rbac.WorkspaceOwner {
		return roleError{"invite people as admin, editor or viewer; make them an owner afterwards"}
	}
	return nil
}

// wouldLeaveNoOwner reports whether turning target (currently `current`)
// into `next` ("" means removed) leaves a workspace that had owners with
// none. A workspace with no owner at all (legacy, unowned) is left alone.
func wouldLeaveNoOwner(owners int, current, next rbac.WorkspaceRole) bool {
	return owners > 0 && current == rbac.WorkspaceOwner && next != rbac.WorkspaceOwner && owners == 1
}

// cleanName validates and trims a workspace name.
func cleanName(s string) (string, error) {
	name := strings.TrimSpace(s)
	n := len([]rune(name))
	if n < 1 || n > 60 {
		return "", errors.New("name must be 1-60 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("name must not contain control characters")
		}
	}
	return name, nil
}

// normalizeEmail lower-cases and checks an email address shape. It is a
// sanity check, not RFC validation: the invite is only useful to someone who
// can sign in with that exact address.
func normalizeEmail(s string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(s))
	if len(e) < 3 || len(e) > 254 || strings.ContainsAny(e, " \t\r\n<>,;\"") {
		return "", false
	}
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return e, true
}

// slugify makes a URL-safe base from a name: lower-case letters and digits
// separated by single dashes, at most 30 characters.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if s == "" {
		s = "workspace"
	}
	return s
}

// newSlug is slugify plus a random 6-hex suffix; the unique constraint on
// workspaces.slug is the backstop for the (unlikely) collision.
func newSlug(name string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", slugify(name), hex.EncodeToString(b)), nil
}

const inviteTokenPrefix = "hinv_"

// newInviteToken returns a one-time token (shown to the inviter once) and
// the hash that is stored.
func newInviteToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = inviteTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
