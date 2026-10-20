// Package scripts holds only tests of the shell scripts next to it (the scripts need no Go).
package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func run(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"local_stack.sh"}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTheLocalStackRefusesDataInsideTheRepoAndAnythingThatIsNotLocal(t *testing.T) {
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	inside := filepath.Join(repo, "tmp-should-never-exist")
	out, err := run(t, []string{"HELIOS_LOCAL_DIR=" + inside}, "status")
	if err == nil || !strings.Contains(out, "inside the repository") {
		t.Errorf("a data directory inside the repo: %v %s", err, out)
	}
	if _, statErr := os.Stat(inside); statErr == nil {
		os.Remove(inside)
		t.Error("the refused directory was created anyway")
	}
	for _, env := range [][]string{{"HELIOS_PG_HOST=db.example.com"}, {"HELIOS_REDIS_HOST=10.0.0.5"}, {"LISTEN_ADDR=0.0.0.0:8080"}} {
		out, err := run(t, append(env, "HELIOS_LOCAL_DIR="+t.TempDir()), "status")
		if err == nil || !strings.Contains(out, "local_stack:") {
			t.Errorf("%v was accepted: %v %s", env, err, out)
		}
	}
	if out, err := run(t, []string{"HELIOS_LOCAL_DIR=" + t.TempDir()}, "bogus"); err == nil || !strings.Contains(out, "usage") {
		t.Errorf("an unknown command: %v %s", err, out)
	}
}

// The scripts carry no secret: every secret-looking assignment is generated at run time.
func TestTheLocalScriptsContainNoSecrets(t *testing.T) {
	files, _ := filepath.Glob("*.sh")
	if len(files) < 5 {
		t.Fatalf("scripts: %v", files)
	}
	bad := regexp.MustCompile(`(?i)(secret|password|token|api_?key)[A-Za-z_]*=['"]?[A-Za-z0-9+/=_-]{16,}['"]?\s*$|postgres://[^@\s/]+:[^@\s]+@|eyJ[A-Za-z0-9_-]{20,}|-----BEGIN`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.Contains(line, "$(") || strings.Contains(line, "${") {
				continue
			}
			if bad.MatchString(line) {
				t.Errorf("%s:%d looks like a secret: %s", f, i+1, line)
			}
		}
	}
}
