package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitopsagent "github.com/oddsund/gitops-agent"
)

// stubSystemctl puts a fake systemctl on PATH that appends every
// invocation's argv to a log file and exits 0, except for "is-active"
// which exits 0 or 1 depending on active.
func stubSystemctl(t *testing.T, active bool) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "systemctl.log")
	activeExit := "1"
	if active {
		activeExit = "0"
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\nif [ \"$1\" = \"is-active\" ]; then\n  exit %s\nfi\nexit 0\n", logPath, activeExit)
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return logPath
}

func testConfig(dir string) Config {
	return Config{
		User:                  "pi",
		RepoURL:               "git@github.com:example/homelab.git",
		SSHKeyPath:            "/home/pi/.ssh/id_ed25519",
		ClonePath:             "/opt/gitops-agent/repo",
		ConfigPath:            filepath.Join(dir, "config.toml"),
		ServiceUnitPath:       filepath.Join(dir, "gitops-agent.service"),
		UpdateServiceUnitPath: filepath.Join(dir, "gitops-agent-update.service"),
		UpdateTimerUnitPath:   filepath.Join(dir, "gitops-agent-update.timer"),
		ServiceName:           "gitops-agent",
		UpdateTimerUnit:       "gitops-agent-update.timer",
	}
}

func TestInstall_WritesConfigWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	logPath := stubSystemctl(t, false)
	cfg := testConfig(dir)

	if err := Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("reading config.toml: %v", err)
	}
	want := []string{
		`repo_url = "git@github.com:example/homelab.git"`,
		`ssh_key_path = "/home/pi/.ssh/id_ed25519"`,
		`clone_path = "/opt/gitops-agent/repo"`,
	}
	for _, w := range want {
		if !strings.Contains(string(got), w) {
			t.Errorf("config.toml missing %q\n--- got ---\n%s", w, got)
		}
	}

	svc, err := os.ReadFile(cfg.ServiceUnitPath)
	if err != nil {
		t.Fatalf("reading gitops-agent.service: %v", err)
	}
	if !strings.Contains(string(svc), "\nUser=pi\n") {
		t.Errorf("gitops-agent.service missing User=pi:\n%s", svc)
	}
	if !strings.Contains(string(svc), "\nReadWritePaths=/opt/gitops-agent/repo\n") {
		t.Errorf("gitops-agent.service missing ReadWritePaths=/opt/gitops-agent/repo:\n%s", svc)
	}

	updateSvc, err := os.ReadFile(cfg.UpdateServiceUnitPath)
	if err != nil || string(updateSvc) != gitopsagent.UpdateServiceUnit {
		t.Errorf("gitops-agent-update.service not written verbatim from the embedded template (err=%v)", err)
	}
	updateTimer, err := os.ReadFile(cfg.UpdateTimerUnitPath)
	if err != nil || string(updateTimer) != gitopsagent.UpdateTimerUnit {
		t.Errorf("gitops-agent-update.timer not written verbatim from the embedded template (err=%v)", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading systemctl log: %v", err)
	}
	if !strings.Contains(string(log), "restart gitops-agent\n") {
		t.Errorf("systemctl log = %q, want a restart since the service wasn't active", log)
	}
	if !strings.Contains(string(log), "enable --now gitops-agent-update.timer\n") {
		t.Errorf("systemctl log = %q, want the update timer enabled", log)
	}
}

func TestInstall_LeavesExistingConfigUntouched_ReadWritePathsFromDisk(t *testing.T) {
	dir := t.TempDir()
	stubSystemctl(t, true)
	cfg := testConfig(dir)

	existing := `[git]
repo_url = "git@github.com:someone-else/other-config.git"
branch = "main"
clone_path = "/srv/gitops/repo"
pull_interval_seconds = 300

[sops]
ssh_key_path = "/home/someone-else/.ssh/id_ed25519"
`
	if err := os.WriteFile(cfg.ConfigPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	// cfg's own RepoURL/SSHKeyPath/ClonePath deliberately differ from what's
	// on disk above -- Install must not let them win.
	if err := Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := os.ReadFile(cfg.ConfigPath)
	if err != nil || string(got) != existing {
		t.Errorf("config.toml was modified, want it left exactly as it was:\n%s", got)
	}

	svc, err := os.ReadFile(cfg.ServiceUnitPath)
	if err != nil {
		t.Fatalf("reading gitops-agent.service: %v", err)
	}
	if !strings.Contains(string(svc), "\nReadWritePaths=/srv/gitops/repo\n") {
		t.Errorf("ReadWritePaths should come from the config.toml on disk (/srv/gitops/repo), got:\n%s", svc)
	}
}

func TestInstall_NoRestartWhenUnitUnchangedAndActive(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)

	stubSystemctl(t, false)
	if err := Install(cfg); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	logPath := stubSystemctl(t, true) // now report active, and start a fresh log
	if err := Install(cfg); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading systemctl log: %v", err)
	}
	if strings.Contains(string(log), "restart") {
		t.Errorf("systemctl log = %q, want no restart on an unchanged, already-active re-run", log)
	}
}

func TestInstall_RestartsWhenUnitContentChanges(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)

	stubSystemctl(t, false)
	if err := Install(cfg); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	cfg.User = "someoneelse" // changes the rendered User= line
	logPath := stubSystemctl(t, true)
	if err := Install(cfg); err != nil {
		t.Fatalf("second Install: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading systemctl log: %v", err)
	}
	if !strings.Contains(string(log), "restart gitops-agent\n") {
		t.Errorf("systemctl log = %q, want a restart since the rendered unit changed", log)
	}
}

// TestRenderServiceUnit_OnlySubstitutesUserAndReadWritePaths is the
// characterization check for ga-tqa.3's "matches what provision.bash
// produces today" acceptance criterion: against the real embedded
// template, only the User= and ReadWritePaths= lines may change -- every
// other line, comments included, must survive byte-for-byte, the same
// guarantee provision.bash's own `sed -e "s/^User=.*/.../"` gives.
func TestRenderServiceUnit_OnlySubstitutesUserAndReadWritePaths(t *testing.T) {
	got := renderServiceUnit(gitopsagent.ServiceUnitTemplate, "pi", "/opt/gitops-agent/repo")

	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(gitopsagent.ServiceUnitTemplate, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("rendered unit has %d lines, template has %d -- substitution must not add or remove lines", len(gotLines), len(wantLines))
	}

	for i, wantLine := range wantLines {
		gotLine := gotLines[i]
		switch {
		case strings.HasPrefix(wantLine, "User="):
			if gotLine != "User=pi" {
				t.Errorf("line %d = %q, want %q", i, gotLine, "User=pi")
			}
		case strings.HasPrefix(wantLine, "ReadWritePaths="):
			if gotLine != "ReadWritePaths=/opt/gitops-agent/repo" {
				t.Errorf("line %d = %q, want %q", i, gotLine, "ReadWritePaths=/opt/gitops-agent/repo")
			}
		default:
			if gotLine != wantLine {
				t.Errorf("line %d changed unexpectedly:\n got:  %q\n want: %q", i, gotLine, wantLine)
			}
		}
	}
}

func TestRenderConfigTOML_SubstitutesRepoURLSSHKeyPathClonePath(t *testing.T) {
	got := renderConfigTOML(gitopsagent.ConfigExampleTOML, "git@github.com:x/y.git", "/home/x/.ssh/id_ed25519", "/opt/x/repo")

	for _, want := range []string{
		`repo_url = "git@github.com:x/y.git"`,
		`ssh_key_path = "/home/x/.ssh/id_ed25519"`,
		`clone_path = "/opt/x/repo"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config.toml missing %q\n--- got ---\n%s", want, got)
		}
	}

	// The comment block above [git] must survive untouched.
	if !strings.Contains(got, "# Example config.toml for gitops-agent.") {
		t.Errorf("rendered config.toml lost its leading comment block")
	}
}
