package integration

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tokenMarkedOperations reads openapi.yaml line by line (the file's shape is
// regular: paths at two spaces, methods at four, a security line at six) and
// returns "METHOD /path" for every operation whose security lists apiToken.
func tokenMarkedOperations(doc string) []string {
	method := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	var out []string
	path, op, inPaths := "", "", false
	for _, line := range strings.Split(doc, "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && len(line) > 0 && line[0] != ' ' && line[0] != '#':
			inPaths = false // the next top-level key (components:)
		}
		if !inPaths {
			continue
		}
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			path, op = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
			continue
		}
		if m := method.FindStringSubmatch(line); m != nil {
			op = strings.ToUpper(m[1])
			continue
		}
		if op != "" && strings.HasPrefix(line, "      security:") && strings.Contains(line, "apiToken") {
			out = append(out, op+" "+path)
		}
	}
	sort.Strings(out)
	return out
}

// The OpenAPI file marks an operation token-accessible exactly when the router
// registry allows tokens on it: neither side may drift from the other.
func TestOpenAPITokenMarksMatchTheRouterRegistry(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	marked := map[string]bool{}
	for _, m := range tokenMarkedOperations(string(raw)) {
		marked[m] = true
	}
	allowed := map[string]bool{}
	w := newTokWorld(t, nil)
	for _, rt := range w.h.routes {
		if rt.TokenAllowed {
			allowed[rt.Method+" "+rt.Path] = true
		}
	}
	if len(allowed) < 20 || len(marked) < 20 {
		t.Fatalf("suspiciously few: %d allowed, %d marked (is the parser broken?)", len(allowed), len(marked))
	}
	for p := range allowed {
		if !marked[p] {
			t.Errorf("%s is token-allowed by the router but not marked token-accessible in openapi.yaml", p)
		}
	}
	for p := range marked {
		if !allowed[p] {
			t.Errorf("%s is marked token-accessible in openapi.yaml but the router does not allow tokens on it", p)
		}
	}
}

func TestTheOpenAPIParserFindsMarksAndNothingElse(t *testing.T) {
	doc := "paths:\n  /a:\n    get:\n      security: [{ supabaseJWT: [] }, { apiToken: [] }]\n    post:\n      security: [{ supabaseJWT: [] }]\n  /b/{id}:\n    delete:\n      security: [{ apiToken: [] }]\ncomponents:\n  x:\n    get:\n      security: [{ apiToken: [] }]\n"
	got := strings.Join(tokenMarkedOperations(doc), ",")
	if got != "DELETE /b/{id},GET /a" {
		t.Errorf("parsed %q", got)
	}
}
