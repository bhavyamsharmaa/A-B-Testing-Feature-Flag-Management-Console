package rbac

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecideMembership(t *testing.T) {
	mine, other := "ws-mine", "ws-other"
	cases := []struct {
		name     string
		memberOf *string
		byUserID bool
		want     membershipDecision
	}{
		{"in my workspace, by userId", &mine, true, membershipAllow},
		{"in my workspace, by email (role change)", &mine, false, membershipAllow},
		{"no workspace, by userId", nil, true, membershipAdopt},
		{"no workspace, by email only", nil, false, membershipDeny},
		{"another workspace, by userId", &other, true, membershipDeny},
		{"another workspace, by email", &other, false, membershipDeny},
	}
	for _, c := range cases {
		if got := decideMembership(c.memberOf, mine, c.byUserID); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// A denied target is reported with the same body as an unknown user.
func TestUnavailableUserResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUserNotFound(rec)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"USER_NOT_FOUND"`) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}
