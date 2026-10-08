package auditlog

import (
	"net/url"
	"strings"
	"testing"
)

func parse(t *testing.T, raw string) (listParams, error) {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("bad test query %q: %v", raw, err)
	}
	return parseListParams(q)
}

func TestLimit(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 50},
		{"limit=1", 1},
		{"limit=200", 200},
		{"limit=201", 200},
		{"limit=100000", 200},
	} {
		p, err := parse(t, tc.query)
		if err != nil || p.Limit != tc.want {
			t.Errorf("%q: limit=%d err=%v, want %d", tc.query, p.Limit, err, tc.want)
		}
	}
	for _, q := range []string{"limit=0", "limit=-5", "limit=abc", "limit=", "limit=1.5", "limit=99999999999999999999", "limit=1&limit=2"} {
		if _, err := parse(t, q); err == nil {
			t.Errorf("%q: want an error", q)
		}
	}
}

func TestCursor(t *testing.T) {
	p, err := parse(t, "before=123")
	if err != nil || p.Before != 123 {
		t.Fatalf("before=123: got %d, %v", p.Before, err)
	}
	if p, _ := parse(t, ""); p.Before != 0 {
		t.Errorf("no cursor should leave Before at 0, got %d", p.Before)
	}
	for _, q := range []string{"before=0", "before=-1", "before=abc", "before=", "before=1e3", "before=9223372036854775808"} {
		if _, err := parse(t, q); err == nil {
			t.Errorf("%q: want an error", q)
		}
	}
}

func TestFilters(t *testing.T) {
	p, err := parse(t, "action=flag.kill&resourceType=flag&resourceId=ui-test-flag&severity=critical")
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != "flag.kill" || p.ResourceType != "flag" || p.ResourceID != "ui-test-flag" || p.Severity != "critical" {
		t.Errorf("filters not captured: %+v", p)
	}

	for _, q := range []string{
		"severity=warning", "severity=INFO", "severity=",
		"action=", "action=flag kill", "action=" + url.QueryEscape("flag.kill';--"), "action=" + strings.Repeat("a", 65),
		"resourceType=", "resourceType=a/b",
		"resourceId=", "resourceId=" + strings.Repeat("a", 257), "resourceId=%00", "resourceId=%ff",
		"action=a&action=b",
	} {
		if _, err := parse(t, q); err == nil {
			t.Errorf("%q: want an error", q)
		}
	}

	// resourceId is free text (user ids, flag keys), so odd characters are fine.
	if _, err := parse(t, "resourceId="+url.QueryEscape("a b/c:d")); err != nil {
		t.Errorf("resourceId with spaces and slashes should be accepted: %v", err)
	}
	// Unknown parameters are ignored.
	if _, err := parse(t, "foo=bar"); err != nil {
		t.Errorf("unknown parameter should be ignored: %v", err)
	}
}

func TestBuildListQuery(t *testing.T) {
	hostile := "x' OR '1'='1"
	p := listParams{Limit: 50, Before: 99, Action: "flag.kill", ResourceType: "flag", ResourceID: hostile, Severity: "critical"}
	sql, args := buildListQuery("22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111", p)

	wantArgs := []any{"22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111", int64(99), "flag.kill", "flag", hostile, "critical", 51}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("arg %d = %v, want %v", i+1, args[i], wantArgs[i])
		}
	}
	for _, frag := range []string{
		"workspace_id = $1::uuid", "environment_id = $2::uuid", "environment_id IS NULL", "id < $3", "action = $4",
		"resource_type = $5", "resource_id = $6", "severity = $7", "ORDER BY id DESC", "LIMIT $8",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("SQL is missing %q:\n%s", frag, sql)
		}
	}
	// Parameterized: no user value may appear in the SQL text.
	for _, v := range []string{hostile, "flag.kill", "critical", "99"} {
		if strings.Contains(sql, v) {
			t.Errorf("value %q leaked into the SQL text", v)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "OFFSET") {
		t.Error("keyset pagination must not use OFFSET")
	}
}

func TestBuildListQueryMinimal(t *testing.T) {
	sql, args := buildListQuery("ws-id", "env-id", listParams{Limit: 10})
	if len(args) != 3 || args[0] != "ws-id" || args[1] != "env-id" || args[2] != 11 {
		t.Fatalf("args = %v, want [ws-id env-id 11]", args)
	}
	if strings.Contains(sql, "id <") || strings.Contains(sql, "action =") {
		t.Errorf("unfiltered query should only scope by environment:\n%s", sql)
	}
	// Global entries (flag.create) are visible in every environment's log.
	if !strings.Contains(sql, "OR environment_id IS NULL") || !strings.Contains(sql, "'global'") {
		t.Errorf("query should include and mark environment-less entries:\n%s", sql)
	}
	if !strings.Contains(sql, "LIMIT $3") {
		t.Errorf("limit placeholder should be $3:\n%s", sql)
	}
}

// The environment-less rows are only visible inside the caller's workspace:
// the workspace condition must be a top-level AND, never inside the OR with
// "environment_id IS NULL", or one tenant would see every tenant's rows.
func TestBuildListQueryScopesNullEnvironmentRowsToWorkspace(t *testing.T) {
	sql, _ := buildListQuery("ws-id", "env-id", listParams{Limit: 10})
	want := "workspace_id = $1::uuid AND (environment_id = $2::uuid OR environment_id IS NULL)"
	if !strings.Contains(sql, want) {
		t.Errorf("query must contain %q:\n%s", want, sql)
	}
}
