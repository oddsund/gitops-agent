package gitsync

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMainIsolatesHostGit guards the TestMain isolation: with a hostile
// global config or inherited GIT_DIR/GIT_INDEX_FILE it fails if the
// isolation is removed.
func TestMainIsolatesHostGit(t *testing.T) {
	for _, k := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_CONFIG_PARAMETERS"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q is set, want unset", k, v)
		}
	}

	// Run outside any repository so only global and system config can appear.
	cmd := exec.Command("git", "config", "--list", "--show-origin")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --list: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "file:") {
			t.Errorf("config read from a file outside the test: %s", line)
		}
	}

	_ = newRemote(t)
}
