package workspaces

import (
	"encoding/json"
	"strings"
	"testing"

	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/controlplane/rbac"
	"helios/backend/internal/platform/auth"
)

func TestDefaultEnvironmentsPlan(t *testing.T) {
	plan, err := provisionPlan(defaultEnvironments)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	prod := 0
	for _, e := range plan {
		keys = append(keys, e.Key)
		if e.IsProduction {
			prod++
			if e.Key != "production" {
				t.Errorf("%s flagged as production", e.Key)
			}
		}
	}
	if strings.Join(keys, ",") != "dev,staging,production" || prod != 1 {
		t.Errorf("plan = %v (production flags: %d)", keys, prod)
	}
	if len(plan) > quota.MaxEnvironments {
		t.Errorf("default plan exceeds the environment quota")
	}
}

func TestProvisionPlanRejectsBadPlans(t *testing.T) {
	cases := map[string][]environmentSpec{
		"over quota":   {{"a", "A", false}, {"b", "B", false}, {"c", "C", false}, {"d", "D", false}},
		"uuid key":     {{"11111111-1111-1111-1111-111111111111", "X", false}},
		"upper case":   {{"Dev", "Dev", false}},
		"empty key":    {{"", "Dev", false}},
		"duplicate":    {{"dev", "A", false}, {"dev", "B", false}},
		"with a slash": {{"a/b", "AB", false}},
	}
	for name, specs := range cases {
		if _, err := provisionPlan(specs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWorkspaceName(t *testing.T) {
	for in, want := range map[string]string{
		"alice@example.com":               "alice's workspace",
		"  bob@x.io ":                     "bob's workspace",
		"":                                "My workspace",
		"@example.com":                    "My workspace",
		strings.Repeat("a", 60) + "@x.io": strings.Repeat("a", 40) + "'s workspace",
	} {
		if got := workspaceName(in); got != want {
			t.Errorf("workspaceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMeShape(t *testing.T) {
	list := []Workspace{
		{ID: "w1", Name: "alice's workspace", Slug: "alice-abc123", Role: rbac.WorkspaceOwner, Environments: []Environment{
			{ID: "e1", Key: "dev", Name: "Development"},
			{ID: "e2", Key: "production", Name: "Production", IsProduction: true},
		}},
		{ID: "w2", Name: "Acme", Slug: "acme-def456", Role: rbac.WorkspaceViewer, Environments: []Environment{}},
	}
	invites := []PendingInvite{{ID: "i1", WorkspaceID: "w3", WorkspaceName: "Globex", Role: rbac.WorkspaceEditor, InvitedByEmail: "bob@example.com"}}
	b, err := json.Marshal(buildMe(auth.User{ID: "u1", Email: "alice@example.com"}, list, invites))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["activeWorkspaceId"] != "w1" {
		t.Errorf("active workspace should be the first (most recent): %s", b)
	}
	wss := got["workspaces"].([]any)
	if len(wss) != 2 {
		t.Fatalf("workspaces: %s", b)
	}
	w0 := wss[0].(map[string]any)
	for _, k := range []string{"id", "name", "slug", "role", "environments"} {
		if _, ok := w0[k]; !ok {
			t.Errorf("workspace is missing %q: %s", k, b)
		}
	}
	e1 := w0["environments"].([]any)[1].(map[string]any)
	if e1["isProduction"] != true || e1["key"] != "production" {
		t.Errorf("environment shape: %s", b)
	}
	inv := got["invites"].([]any)[0].(map[string]any)
	if inv["workspaceName"] != "Globex" || inv["role"] != "editor" || inv["id"] != "i1" {
		t.Errorf("invite shape: %s", b)
	}
	if _, leaked := inv["token"]; leaked {
		t.Error("an invite listed in /me must never carry its token")
	}
}

func TestMeShapeNeverNull(t *testing.T) {
	b, _ := json.Marshal(buildMe(auth.User{ID: "u"}, nil, nil))
	for _, want := range []string{`"workspaces":[]`, `"invites":[]`, `"activeWorkspaceId":""`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("want %s in %s", want, b)
		}
	}
}
