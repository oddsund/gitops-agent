package deploy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubDocker puts a fake "docker" script on PATH that records the argv it
// was invoked with (one arg per line) to argsFile, and exits with
// exitCode. This exercises the real os/exec code path without needing a
// working docker daemon in CI.
func stubDocker(t *testing.T, exitCode int) (argsFile string) {
	t.Helper()
	stubDir := t.TempDir()
	argsFile = filepath.Join(stubDir, "args.txt")

	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + argsFile + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", stubDir)
	return argsFile
}

func TestDeploy_InvokesDockerComposeUp(t *testing.T) {
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	argsFile := stubDocker(t, 0)

	if err := Deploy(serviceDir); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("docker stub was not invoked: %v", err)
	}
	want := "compose\n-f\n" + filepath.Join(serviceDir, "compose.yml") + "\nup\n-d\n--remove-orphans\n"
	if string(got) != want {
		t.Errorf("docker invoked with args:\n%s\nwant:\n%s", got, want)
	}
}

func TestDeploy_SucceedsWithComposeOutput(t *testing.T) {
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	// Real `docker compose up -d` writes its "Container X Started"
	// progress to stderr even on success -- make sure that doesn't get
	// mistaken for a failure.
	script := "#!/bin/sh\necho 'Container demoapp  Started' >&2\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir)

	if err := Deploy(serviceDir); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
}

func TestDeploy_PropagatesFailure(t *testing.T) {
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubDocker(t, 1)

	if err := Deploy(serviceDir); err == nil {
		t.Fatal("expected error when docker compose exits non-zero")
	}
}

func TestDeployWithTimeout_KillsHungInvocation(t *testing.T) {
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	// exec (rather than fork a "sleep" child) so the stub *is* the process
	// exec.CommandContext kills -- no orphaned grandchild left holding the
	// stdout/stderr pipes open, which would otherwise stall Run() for the
	// full WaitDelay.
	script := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	err := DeployWithTimeout(serviceDir, 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from hung docker invocation")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("DeployWithTimeout took %s, want it to return promptly after the timeout", elapsed)
	}
}
