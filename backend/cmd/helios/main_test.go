package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every command in the image is a real cmd/ directory, and every job cmd/ directory is reachable from the image.
func TestTheCommandTableMatchesTheCmdDirectories(t *testing.T) {
	entries, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			have[e.Name()] = true
		}
	}
	for name, dir := range Commands {
		if !have[dir] {
			t.Errorf("command %q names cmd/%s, which does not exist", name, dir)
		}
	}
	// what the image deliberately leaves out: the dispatcher itself, the development auth stand-in, the key minting tool
	for dir := range have {
		reachable := dir == "helios" || dir == "devauth" || dir == "mkkey"
		for _, d := range Commands {
			if d == dir {
				reachable = true
			}
		}
		if !reachable {
			t.Errorf("cmd/%s is in no image command and is not on the list of deliberate omissions", dir)
		}
	}
}

func TestUnknownCommandsExitNonZeroAndTheListIsPrinted(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "helios")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := exec.Command(bin, "rm-rf").CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "scheduler") {
		t.Errorf("unknown command: %v %s", err, out)
	}
	out, err = exec.Command(bin, "commands").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "purge-workspaces") || !strings.Contains(string(out), "healthcheck") {
		t.Errorf("commands: %v %s", err, out)
	}
}

// The chosen command replaces the dispatcher and gets its arguments and environment unchanged.
func TestTheDispatcherExecsTheCommandWithItsArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "helios")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	fake := filepath.Join(dir, "bin")
	if err := os.Mkdir(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"ran $0 $@ env=$MARKER\"\n"
	if err := os.WriteFile(filepath.Join(fake, "scheduler"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "scheduler", "-grace", "10m")
	cmd.Env = append(os.Environ(), "HELIOS_BIN_DIR="+fake, "MARKER=yes")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "/bin/scheduler -grace 10m env=yes") {
		t.Errorf("%v %q", err, out)
	}
}
