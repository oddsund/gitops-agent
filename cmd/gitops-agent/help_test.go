package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestBuiltBinaryHelpListsUpdate builds the real binary and runs `-h`, so
// the flag.Usage wiring in main is covered, not just printDaemonUsage.
// systemd/update.bash depends on this output to recognise a binary that has
// the update subcommand.
func TestBuiltBinaryHelpListsUpdate(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "gitops-agent")
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h exited non-zero: %v\n%s", err, out)
	}
	if !slices.Contains(strings.Split(string(out), "\n"), "  update") {
		t.Errorf("-h output = %q, want a line %q", out, "  update")
	}
}
