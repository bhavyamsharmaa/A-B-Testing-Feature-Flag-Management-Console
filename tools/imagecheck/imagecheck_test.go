package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // (the umask would otherwise strip the bits under test)
		t.Fatal(err)
	}
}

// cleanImage is what the Dockerfile's final stage produces.
func cleanImage(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "app/helios", "\x7fELF-binary-with-nothing-secret", 0o755)
	write(t, root, "app/bin/api", "\x7fELF-binary", 0o755)
	write(t, root, "etc/passwd", "helios:x:65532:65532:helios:/nonexistent:/sbin/nologin\n", 0o644)
	write(t, root, "etc/group", "helios:x:65532:\n", 0o644)
	write(t, root, "etc/ssl/certs/ca-certificates.crt", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n", 0o644)
	return root
}

func findings(t *testing.T, root string) []Finding {
	t.Helper()
	files, err := FromRootfs(root)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Check(files)
	return f
}

func TestACleanImagePasses(t *testing.T) {
	if f := findings(t, cleanImage(t)); len(f) != 0 {
		t.Errorf("findings in a clean image: %+v", f)
	}
}

func TestEverythingThatMustNotShipIsFound(t *testing.T) {
	for name, c := range map[string]struct {
		rel, body string
		mode      os.FileMode
		why       string
	}{
		"an .env":                     {"app/.env", "A=b", 0o644, "environment"},
		"an .env.local":               {"app/.env.local", "A=b", 0o644, "environment"},
		"a secrets file":              {"app/secrets.env", "A=b", 0o644, "environment"},
		"a private key file":          {"app/tls.key", "x", 0o600, "key"},
		"an ssh key":                  {"root/.ssh/id_rsa", "x", 0o600, "key"},
		"source":                      {"app/main.go", "package main", 0o644, "source"},
		"go.mod":                      {"app/go.mod", "module x", 0o644, "source"},
		"a test":                      {"app/foo_test.go", "package x", 0o644, "source"},
		"test data":                   {"app/testdata/golden.json", "{}", 0o644, "source"},
		"a migration":                 {"app/db/0001.sql", "select 1", 0o644, "source"},
		"a file outside the app":      {"usr/bin/curl", "x", 0o755, "outside"},
		"a world-writable file":       {"app/tmp.txt", "x", 0o666, "writable by everyone"},
		"a private key in text":       {"app/notes.txt", "-----BEGIN RSA PRIVATE KEY-----\nabc", 0o644, "private key"},
		"a database password":         {"app/conf.txt", "postgres://admin:hunter22@db.example.com/prod", 0o644, "database URL"},
		"a redis password":            {"app/conf2.txt", "rediss://default:s3cretpw@host:6379", 0o644, "Redis URL"},
		"an SDK key":                  {"app/k.txt", "hlt_sdk_abcdefghij0123456789abcdef", 0o644, "SDK"},
		"a webhook secret":            {"app/w.txt", "whsec_0123456789abcdef0123456789", 0o644, "webhook"},
		"a JWT":                       {"app/j.txt", "eyJhbGciOiJFUzI1NiJ9AAAAAAAAAA.eyJzdWIiOiIxMjM0NTY3ODkwAAAAA.signaturesignature", 0o644, "web token"},
		"an AWS key":                  {"app/a.txt", "AKIAABCDEFGHIJKLMNOP", 0o644, "AWS"},
		"the same secret in a binary": {"app/bin/api", "\x00\x00postgres://u:pass1234@h/db\x00", 0o755, "database URL"},
	} {
		root := cleanImage(t)
		write(t, root, c.rel, c.body, c.mode)
		got := findings(t, root)
		hit := false
		for _, f := range got {
			if strings.Contains(f.Why, c.why) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("%s: not found (findings: %+v)", name, got)
		}
	}
}

func TestATarStreamIsCheckedLikeADirectory(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{{"app/helios", "elf"}, {"etc/passwd", "helios:x:65532:65532::/:/sbin/nologin\n"}, {"app/.env", "SECRET=1"}} {
		_ = tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()
	files, err := FromTar(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Check(files)
	if len(got) != 1 || got[0].Path != "/app/.env" {
		t.Errorf("findings: %+v", got)
	}
}

// ---- the Dockerfile and .dockerignore themselves -----------------------------------------------------

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "backend", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheDockerfileIsPinnedStaticNonRootAndCopiesOnlyTheSource(t *testing.T) {
	df := read(t, "Dockerfile")
	var lines []string
	for _, l := range strings.Split(df, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	text := strings.Join(lines, "\n")
	// base images: an exact version, never latest
	from := regexp.MustCompile(`(?m)^FROM\s+(\S+)`).FindAllStringSubmatch(text, -1)
	if len(from) < 2 {
		t.Fatalf("expected a build stage and a final stage, got %v", from)
	}
	for _, m := range from[:len(from)-1] {
		img := m[1]
		if strings.Contains(img, ":latest") || !regexp.MustCompile(`:[^$]*\d+\.\d+\.\d+|:\$\{[A-Z_]+\}-`).MatchString(img) && !strings.Contains(img, "@sha256:") {
			t.Errorf("base image %q is not pinned to an exact version", img)
		}
	}
	if m := regexp.MustCompile(`(?m)^ARG GO_VERSION=(\d+\.\d+\.\d+)$`).FindStringSubmatch(text); m == nil {
		t.Error("GO_VERSION must be an exact x.y.z")
	}
	if last := from[len(from)-1][1]; last != "scratch" && !strings.Contains(last, "distroless") {
		t.Errorf("the final stage is %q, want scratch or distroless", last)
	}
	for _, must := range []string{"CGO_ENABLED=0", "-trimpath", "go mod verify", "USER 65532:65532", "ENTRYPOINT", "HEALTHCHECK"} {
		if !strings.Contains(text, must) {
			t.Errorf("the Dockerfile lacks %q", must)
		}
	}
	// the final stage never runs as root
	final := text[strings.LastIndex(text, "FROM scratch"):]
	if !regexp.MustCompile(`(?m)^USER (\d+):(\d+)$`).MatchString(final) || strings.Contains(final, "USER 0") || strings.Contains(final, "USER root") {
		t.Error("the final stage must set a numeric non-root USER")
	}
	// nothing is copied wholesale, nothing is fetched at build time into the final image
	for _, bad := range []string{"COPY . ", "COPY .\n", "ADD ", "COPY --from=build /src", "curl ", "wget ", "apt-get", "ENV DATABASE_URL", "ENV SUPABASE", "_SECRET", "PASSWORD", "API_KEY"} {
		if strings.Contains(text, bad) {
			t.Errorf("the Dockerfile contains %q", bad)
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^COPY\s+(?:--from=\S+\s+)?(\S+)`).FindAllStringSubmatch(text, -1) {
		src := m[1]
		if strings.HasPrefix(src, "/") {
			continue // from a build stage
		}
		switch src {
		case "go.mod", "cmd", "internal":
		default:
			t.Errorf("COPY of %q from the build context: only go.mod, go.sum, cmd and internal are allowed", src)
		}
	}
	if !strings.Contains(text, "HELIOS_ENV=production") {
		t.Error("the image must default to production mode")
	}
}

func TestTheDockerignoreKeepsSecretsTestsAndTestDataOutOfTheBuildContext(t *testing.T) {
	ign := read(t, ".dockerignore")
	for _, want := range []string{".env", ".env.*", "*.pem", "*.key", "secrets.env", "**/*_test.go", "**/testdata", ".git", "db", "scripts"} {
		found := false
		for _, l := range strings.Split(ign, "\n") {
			if strings.TrimSpace(l) == want {
				found = true
			}
		}
		if !found {
			t.Errorf(".dockerignore lacks %q", want)
		}
	}
}
