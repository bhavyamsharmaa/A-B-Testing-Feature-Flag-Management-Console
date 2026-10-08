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
	ws := Workspace{ID: "w1", Name: "alice's workspace", Environments: []Environment{
		{ID: "e1", Key: "dev", Name: "Development", Role: rbac.Admin},
		{ID: "e2", Key: "production", Name: "Production", IsProduction: true, Role: rbac.Viewer},
	}}
	b, err := json.Marshal(buildMe(auth.User{ID: "u1", Email: "alice@example.com"}, ws))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["workspace"].(map[string]any)["id"] != "w1" {
		t.Errorf("workspace missing: %s", b)
	}
	envs := got["environments"].([]any)
	if len(envs) != 2 {
		t.Fatalf("environments: %s", b)
	}
	e0 := envs[0].(map[string]any)
	for _, k := range []string{"id", "key", "name", "isProduction", "role"} {
		if _, ok := e0[k]; !ok {
			t.Errorf("environment is missing %q: %s", k, b)
		}
	}
	// The deployed console still reads `roles`.
	roles := got["roles"].([]any)
	if len(roles) != 2 || roles[1].(map[string]any)["environment"] != "production" || roles[1].(map[string]any)["role"] != "viewer" {
		t.Errorf("legacy roles shape changed: %s", b)
	}
}

func TestMeShapeNeverNull(t *testing.T) {
	b, _ := json.Marshal(buildMe(auth.User{ID: "u"}, Workspace{ID: "w"}))
	if !strings.Contains(string(b), `"environments":[]`) || !strings.Contains(string(b), `"roles":[]`) {
		t.Errorf("empty lists must be [], got %s", b)
	}
}
