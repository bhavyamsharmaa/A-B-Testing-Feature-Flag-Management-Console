package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// State is what the scenarios need from a seeded workspace.
type State struct {
	API         string   `json:"api"`
	Token       string   `json:"token"` // the seeding user's session (for flag edits during the stream scenario)
	WorkspaceID string   `json:"workspaceId"`
	EnvID       string   `json:"envId"`
	SDKKey      string   `json:"sdkKey"`
	FlagKeys    []string `json:"flagKeys"`
	Experiments []string `json:"experimentIds"`
	Variations  []string `json:"experimentVariations"`
	EditFlag    string   `json:"editFlag"`
}

var httpc = &http.Client{Timeout: 30 * time.Second}

func call(method, url, token string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := httpc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, fmt.Errorf("%s %s: %v: %s", method, url, err, b)
		}
	}
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("%s %s: %d %s", method, url, res.StatusCode, bytes.TrimSpace(b))
	}
	return res.StatusCode, nil
}

// seed creates a throwaway user and workspace with realistic content: many flags (boolean and multivariate, rollouts,
// targeting rules on attributes, segment rules), segments with members, and running experiments.
func seed(o options) error {
	email := fmt.Sprintf("load-%d@test.dev", time.Now().UnixNano())
	cred := map[string]string{"email": email, "password": "password123"}
	if _, err := call("POST", o.auth+"/auth/v1/signup", "", cred, nil); err != nil {
		return fmt.Errorf("sign up (is devauth running at %s?): %w", o.auth, err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := call("POST", o.auth+"/auth/v1/token?grant_type=password", "", cred, &tok); err != nil {
		return err
	}
	t := tok.AccessToken
	var me struct {
		Workspaces []struct {
			ID           string `json:"id"`
			Environments []struct{ ID, Key string }
		} `json:"workspaces"`
	}
	if _, err := call("GET", o.api+"/me", t, nil, &me); err != nil || len(me.Workspaces) == 0 {
		return fmt.Errorf("/me: %v", err)
	}
	ws := me.Workspaces[0]
	st := State{API: o.api, Token: t, WorkspaceID: ws.ID}
	for _, e := range ws.Environments {
		if e.Key == "dev" {
			st.EnvID = e.ID
		}
	}
	env := o.api + "/environments/" + st.EnvID

	// segments: a rules-only one and one with explicit members
	members := make([]string, 200)
	for i := range members {
		members[i] = fmt.Sprintf("vip-%d", i)
	}
	for _, sg := range []map[string]any{
		{"key": "beta", "name": "Beta", "description": "", "members": members},
		{"key": "paying", "name": "Paying", "description": "", "members": []string{}, "rules": []map[string]any{{"attribute": "plan", "operator": "equals", "value": "pro"}}},
	} {
		if _, err := call("POST", o.api+"/workspaces/"+ws.ID+"/segments", t, sg, nil); err != nil {
			return err
		}
	}

	keys, err := seedFlags(env, t, o.flags)
	if err != nil {
		return err
	}
	st.FlagKeys = keys
	st.EditFlag = keys[1]

	// five running experiments on enabled multivariate flags (evaluations then also issue exposure tokens)
	for _, k := range []int{4, 9, 14, 19, 24} {
		fk := st.FlagKeys[k]
		xk := fmt.Sprintf("exp-%d", k)
		var x struct{ ID string }
		if _, err := call("POST", env+"/experiments", t, map[string]any{"flagKey": fk, "key": xk, "name": xk,
			"metrics": []map[string]any{{"name": "M", "eventName": "purchase", "type": "conversion", "isPrimary": true}}}, &x); err != nil {
			return err
		}
		if _, err := call("POST", env+"/experiments/"+xk+"/start", t, nil, nil); err != nil {
			return err
		}
		st.Experiments = append(st.Experiments, x.ID)
		st.Variations = append(st.Variations, "a")
	}
	var key struct {
		Plaintext string `json:"plaintext"`
	}
	if _, err := call("POST", env+"/sdk-keys", t, map[string]any{"name": "loadtest"}, &key); err != nil {
		return err
	}
	st.SDKKey = key.Plaintext

	if err := os.MkdirAll(filepath.Dir(o.state), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(o.state, b, 0o600); err != nil {
		return err
	}
	for i := 0; i < o.fillers; i++ {
		if err := seedFiller(o, i, 50); err != nil {
			return fmt.Errorf("filler workspace %d: %w", i, err)
		}
	}
	fmt.Printf("seeded workspace %s: %d flags, 2 segments (200 members), %d running experiments, plus %d filler workspaces of 50 flags; state in %s\n", ws.ID, len(st.FlagKeys), len(st.Experiments), o.fillers, o.state)
	return nil
}

// seedFlags creates n flags of five kinds: plain, percentage rollout, attribute rules, segment rules (one extra query
// per evaluation) and multivariate with a rollout.
func seedFlags(env, t string, n int) ([]string, error) {
	var out []string
	bool2 := []map[string]any{{"id": "on", "value": true}, {"id": "off", "value": false}}
	str3 := []map[string]any{{"id": "a", "value": "a"}, {"id": "b", "value": "b"}, {"id": "c", "value": "c"}}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("flag-%04d", i)
		var vars []map[string]any
		vtype := "boolean"
		patch := map[string]any{"enabled": i%10 != 0}
		switch i % 5 {
		case 0:
			vars = bool2
			patch["fallthroughVariationId"] = "on"
		case 1:
			vars = bool2
			patch["rollout"] = map[string]any{"on": 30000, "off": 70000}
		case 2:
			vars = bool2
			patch["fallthroughVariationId"] = "off"
			patch["targetingRules"] = []map[string]any{
				{"clauses": []map[string]any{{"attribute": "country", "operator": "in", "value": []string{"DE", "FR", "NL"}}}, "variationId": "on"},
				{"clauses": []map[string]any{{"attribute": "plan", "operator": "equals", "value": "pro"}, {"attribute": "age", "operator": "greaterThan", "value": 17}}, "variationId": "on"},
			}
		case 3:
			vars = bool2
			patch["fallthroughVariationId"] = "off"
			patch["targetingRules"] = []map[string]any{{"clauses": []map[string]any{{"operator": "inSegment", "value": "beta"}}, "variationId": "on"}}
		default:
			vtype, vars = "string", str3
			patch["rollout"] = map[string]any{"a": 50000, "b": 30000, "c": 20000}
		}
		if _, err := call("POST", env+"/flags", t, map[string]any{"key": key, "name": key, "variationType": vtype, "variations": vars}, nil); err != nil {
			return nil, err
		}
		if _, err := call("PATCH", env+"/flags/"+key, t, patch, nil); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, nil
}

// seedFiller makes another workspace (its own user) with a full set of flags and the segments they reference, so the
// database holds realistic volume beside the workspace under test.
func seedFiller(o options, i, n int) error {
	cred := map[string]string{"email": fmt.Sprintf("filler-%d-%d@test.dev", time.Now().UnixNano(), i), "password": "password123"}
	if _, err := call("POST", o.auth+"/auth/v1/signup", "", cred, nil); err != nil {
		return err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := call("POST", o.auth+"/auth/v1/token?grant_type=password", "", cred, &tok); err != nil {
		return err
	}
	var me struct {
		Workspaces []struct {
			ID           string `json:"id"`
			Environments []struct{ ID, Key string }
		} `json:"workspaces"`
	}
	if _, err := call("GET", o.api+"/me", tok.AccessToken, nil, &me); err != nil || len(me.Workspaces) == 0 {
		return fmt.Errorf("/me: %v", err)
	}
	ws := me.Workspaces[0]
	_, _ = call("POST", o.api+"/workspaces/"+ws.ID+"/segments", tok.AccessToken, map[string]any{"key": "beta", "name": "Beta", "description": "", "members": []string{"a", "b", "c"}}, nil)
	for _, e := range ws.Environments {
		if e.Key == "dev" {
			_, err := seedFlags(o.api+"/environments/"+e.ID, tok.AccessToken, n)
			return err
		}
	}
	return nil
}

func loadState(o options) (State, error) {
	var st State
	b, err := os.ReadFile(o.state)
	if err != nil {
		return st, fmt.Errorf("no seed state at %s: run `go run . seed` first (%w)", o.state, err)
	}
	return st, json.Unmarshal(b, &st)
}
