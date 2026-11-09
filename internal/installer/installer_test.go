package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitopsagent "github.com/oddsund/gitops-agent"
	"github.com/oddsund/gitops-agent/internal/cmdrun"
	"github.com/oddsund/gitops-agent/internal/config"
)

func newTestConfig(t *testing.T) (Config, *cmdrun.Fake, *[]string) {
	t.Helper()
	fake := &cmdrun.Fake{}
	logs := &[]string{}
	return Config{
		Root:       t.TempDir(),
		User:       "pi",
		RepoURL:    "git@github.com:example/services.git",
		SSHKeyPath: "/home/pi/.ssh/id_ed25519",
		ClonePath:  "/opt/gitops-agent/repo",
		Version:    "v1.2.3",
		Runner:     fake,
		Logf:       func(format string, args ...any) { *logs = append(*logs, fmt.Sprintf(format, args...)) },
		EUID:       func() int { return 0 },
		FileUID:    func(fs.FileInfo) (int, bool) { return 0, true },
	}, fake, logs
}

// fakeSystemd answers is-active with an error until a restart of the agent
// has happened (or *active is set), like a unit that was never started.
const showStale = "show -p NeedDaemonReload --value gitops-agent.service"

func fakeSystemd(active *bool) func(cmdrun.Call) ([]byte, error) {
	return func(c cmdrun.Call) ([]byte, error) {
		args := strings.Join(c.Args, " ")
		switch args {
		case showStale:
			return []byte("no\n"), nil
		case "is-active --quiet gitops-agent":
			if !*active {
				return nil, errors.New("inactive")
			}
		case "restart gitops-agent":
			*active = true
		}
		return nil, nil
	}
}

func systemctlCalls(t *testing.T, fake *cmdrun.Fake) []string {
	t.Helper()
	var out []string
	for _, c := range fake.Calls {
		if c.Name != "systemctl" {
			t.Fatalf("unexpected command %q", c.Name)
		}
		if c.Env != nil && len(c.Env) != 0 {
			t.Fatalf("unexpected env %v", c.Env)
		}
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func logContains(logs []string, sub string) bool {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func rootPath(cfg Config, rel string) string { return filepath.Join(cfg.Root, rel) }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertRootEmpty(t *testing.T, cfg Config) {
	t.Helper()
	if names := listDir(t, cfg.Root); len(names) != 0 {
		t.Errorf("root not empty: %v", names)
	}
}

var (
	svcPath   = filepath.Join(unitDir, "gitops-agent.service")
	updPath   = filepath.Join(unitDir, "gitops-agent-update.service")
	timerPath = filepath.Join(unitDir, "gitops-agent-update.timer")
)

func goldenService(t *testing.T) string {
	t.Helper()
	return mustRead(t, "testdata/gitops-agent.service.golden")
}

// asInstalledBinary writes a fake binary at Root/usr/local/bin/gitops-agent
// and makes it "the running executable".
func asInstalledBinary(t *testing.T, cfg *Config) string {
	t.Helper()
	bin := rootPath(*cfg, binPath)
	mustWrite(t, bin, "x", 0o755)
	cfg.Executable = func() (string, error) { return bin, nil }
	return bin
}

func TestInstall_FreshHost(t *testing.T) {
	cfg, fake, logs := newTestConfig(t)
	asInstalledBinary(t, &cfg)
	active := false
	fake.Func = fakeSystemd(&active)

	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	cfgFile := rootPath(cfg, configPath)
	if fi, err := os.Stat(cfgFile); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("config.toml: %v, %v", fi, err)
	}
	content := mustRead(t, cfgFile)
	for _, want := range []string{
		`repo_url = "git@github.com:example/services.git"`,
		`ssh_key_path = "/home/pi/.ssh/id_ed25519"`,
		`clone_path = "/opt/gitops-agent/repo"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config.toml lacks %q", want)
		}
	}
	if _, err := config.Load(cfgFile); err != nil {
		t.Errorf("config.Load: %v", err)
	}

	if got := mustRead(t, rootPath(cfg, svcPath)); got != goldenService(t) {
		t.Errorf("service unit differs from golden:\n%s", got)
	}
	if got := mustRead(t, rootPath(cfg, updPath)); got != gitopsagent.UpdateServiceUnit {
		t.Error("update service differs from embed")
	}
	if got := mustRead(t, rootPath(cfg, timerPath)); got != gitopsagent.UpdateTimerUnit {
		t.Error("update timer differs from embed")
	}
	for _, p := range []string{svcPath, updPath, timerPath, versionFilePath} {
		if fi, err := os.Stat(rootPath(cfg, p)); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, %v", p, fi, err)
		}
	}
	if got := mustRead(t, rootPath(cfg, versionFilePath)); got != "v1.2.3\n" {
		t.Errorf("version file = %q", got)
	}

	want := []string{
		showStale,
		"daemon-reload",
		"enable gitops-agent",
		"is-active --quiet gitops-agent",
		"restart gitops-agent",
		"enable --now gitops-agent-update.timer",
	}
	if got := systemctlCalls(t, fake); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	for _, sub := range []string{"wrote", "restarting gitops-agent"} {
		if !logContains(*logs, sub) {
			t.Errorf("log lacks %q: %v", sub, *logs)
		}
	}
}

type snapshot struct {
	content string
	mode    os.FileMode
	mtime   int64
}

func snap(t *testing.T, path string) snapshot {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot{mustRead(t, path), fi.Mode().Perm(), fi.ModTime().UnixNano()}
}

func TestInstall_Idempotent(t *testing.T) {
	cfg, fake, logs := newTestConfig(t)
	asInstalledBinary(t, &cfg)
	active := false
	fake.Func = fakeSystemd(&active)
	files := []string{configPath, svcPath, updPath, timerPath, versionFilePath}

	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	before := map[string]snapshot{}
	for _, f := range files {
		before[f] = snap(t, rootPath(cfg, f))
	}
	etcBefore := listDir(t, rootPath(cfg, "etc/gitops-agent"))
	unitsBefore := listDir(t, rootPath(cfg, unitDir))

	fake.Calls = nil
	*logs = nil
	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		if after := snap(t, rootPath(cfg, f)); after != before[f] {
			t.Errorf("%s changed on rerun: %+v -> %+v", f, before[f], after)
		}
	}
	want := []string{
		showStale,
		"daemon-reload",
		"enable gitops-agent",
		"is-active --quiet gitops-agent",
		"enable --now gitops-agent-update.timer",
	}
	if got := systemctlCalls(t, fake); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	for _, sub := range []string{"no restart needed", "already exists, leaving it as is", "unchanged"} {
		if !logContains(*logs, sub) {
			t.Errorf("log lacks %q: %v", sub, *logs)
		}
	}
	if got := listDir(t, rootPath(cfg, "etc/gitops-agent")); !equalStrings(got, etcBefore) {
		t.Errorf("etc/gitops-agent listing changed: %v -> %v", etcBefore, got)
	}
	if got := listDir(t, rootPath(cfg, unitDir)); !equalStrings(got, unitsBefore) {
		t.Errorf("unit dir listing changed: %v -> %v", unitsBefore, got)
	}
}

func TestInstall_ExistingConfigWins(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	asInstalledBinary(t, &cfg)
	active := true
	fake.Func = fakeSystemd(&active)
	existing := `[git]
repo_url = "git@example.com:other/repo.git"
branch = "main"
clone_path = "/srv/gitops/repo"
pull_interval_seconds = 300

[sops]
ssh_key_path = "/root/.ssh/id"
`
	cfgFile := rootPath(cfg, configPath)
	mustWrite(t, cfgFile, existing, 0o600)

	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	if got := mustRead(t, cfgFile); got != existing {
		t.Errorf("config.toml was modified:\n%s", got)
	}
	if fi, _ := os.Stat(cfgFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
	svc := mustRead(t, rootPath(cfg, svcPath))
	for _, want := range []string{"\nReadWritePaths=/srv/gitops/repo\n", "\nUser=pi\n"} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit lacks %q", want)
		}
	}
	if got := mustRead(t, rootPath(cfg, versionFilePath)); got != "v1.2.3\n" {
		t.Errorf("version file = %q", got)
	}
}

func TestInstall_ExistingConfigWithoutAnyRepoFlags(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	active := true
	fake.Func = fakeSystemd(&active)
	cfg.RepoURL, cfg.SSHKeyPath, cfg.ClonePath = "", "", ""

	t.Run("without config", func(t *testing.T) {
		err := Install(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("err = %v, want it to mention required", err)
		}
		assertRootEmpty(t, cfg)
		if len(fake.Calls) != 0 {
			t.Errorf("calls = %v, want none", fake.Calls)
		}
	})

	t.Run("with config", func(t *testing.T) {
		cfg := cfg
		cfg.Root = t.TempDir()
		rendered, err := renderConfigTOML(gitopsagent.ConfigExampleTOML, goldenRepoURL, goldenSSHKey, goldenClone)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, rootPath(cfg, configPath), rendered, 0o644)
		if err := Install(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInstall_RestartsWhenUnitChanges(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	active := false
	fake.Func = fakeSystemd(&active)
	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, rootPath(cfg, svcPath), "stale\n", 0o644)
	fake.Calls = nil

	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := systemctlCalls(t, fake); !contains(got, "restart gitops-agent") {
		t.Errorf("calls = %v, want a restart", got)
	}
	if got := mustRead(t, rootPath(cfg, svcPath)); got != goldenService(t) {
		t.Error("unit not restored to the golden rendering")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestInstall_RestartsWhenInactive(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	active := false
	fake.Func = fakeSystemd(&active)
	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	active = false
	fake.Calls = nil

	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := systemctlCalls(t, fake); !contains(got, "restart gitops-agent") {
		t.Errorf("calls = %v, want a restart", got)
	}
}

func TestInstall_UpdateUnitChangeDoesNotRestartAgent(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	active := false
	fake.Func = fakeSystemd(&active)
	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	t.Run("stale update service", func(t *testing.T) {
		mustWrite(t, rootPath(cfg, updPath), "stale\n", 0o644)
		fake.Calls = nil
		if err := Install(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		got := systemctlCalls(t, fake)
		if contains(got, "restart gitops-agent") || contains(got, "restart gitops-agent-update.timer") {
			t.Errorf("calls = %v, want no restart", got)
		}
	})

	t.Run("stale timer", func(t *testing.T) {
		mustWrite(t, rootPath(cfg, timerPath), "stale\n", 0o644)
		fake.Calls = nil
		if err := Install(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		got := systemctlCalls(t, fake)
		if !contains(got, "restart gitops-agent-update.timer") {
			t.Errorf("calls = %v, want a timer restart", got)
		}
		if contains(got, "restart gitops-agent") {
			t.Errorf("calls = %v, want no agent restart", got)
		}
	})
}

func TestInstall_NotRoot(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	cfg.EUID = func() int { return 1 }
	err := Install(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %v", fake.Calls)
	}
	assertRootEmpty(t, cfg)
}

func TestInstall_RejectsInjection(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Config)
	}{
		{"user newline", func(c *Config) { c.User = "pi\nExecStartPre=/bin/evil" }},
		{"user root", func(c *Config) { c.User = "root" }},
		{"clone path newline", func(c *Config) { c.ClonePath = "/x\nExecStartPre=/bin/evil" }},
		{"clone path space", func(c *Config) { c.ClonePath = "/x y" }},
		{"ssh key newline", func(c *Config) { c.SSHKeyPath = "/x\ny" }},
		{"repo url newline", func(c *Config) { c.RepoURL = "git@h:o/r.git\nbranch = \"evil\"" }},
		{"relative root", func(c *Config) { c.Root = "rel" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, fake, _ := newTestConfig(t)
			root := cfg.Root
			tc.mod(&cfg)
			if err := Install(context.Background(), cfg); err == nil {
				t.Fatal("expected an error")
			}
			if len(fake.Calls) != 0 {
				t.Errorf("calls = %v", fake.Calls)
			}
			if names := listDir(t, root); len(names) != 0 {
				t.Errorf("root not empty: %v", names)
			}
		})
	}
}

func TestInstall_HostileExistingConfig(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	hostile := `[git]
repo_url = "git@example.com:o/r.git"
branch = "main"
clone_path = "/x\nExecStartPre=/bin/evil"
pull_interval_seconds = 300

[sops]
ssh_key_path = "/k"
`
	cfgFile := rootPath(cfg, configPath)
	mustWrite(t, cfgFile, hostile, 0o644)

	err := Install(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "clone_path") {
		t.Fatalf("err = %v, want it to mention clone_path", err)
	}
	if _, statErr := os.Stat(rootPath(cfg, svcPath)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("service unit was written")
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %v", fake.Calls)
	}
	if got := mustRead(t, cfgFile); got != hostile {
		t.Error("config.toml was modified")
	}
}

func TestInstall_VersionFile(t *testing.T) {
	cases := []struct {
		name, version, existing string
		runFrom                 string // installed, elsewhere, nobin, symlink, error
		wantFile                string // "" means absent
		wantLog                 string
		wantErr                 string
		existingIsDir           bool
		wantKeepMtime           bool
	}{
		{"absent", "v1.2.3", "", "installed", "v1.2.3\n", "wrote", "", false, false},
		{"stale", "v1.2.3", "v1.0.0\n", "installed", "v1.2.3\n", "updated", "", false, false},
		{"stale garbage", "v1.2.3", "gitops-agent-v1.5.0\n", "installed", "v1.2.3\n", "updated", "", false, false},
		{"equal", "v1.2.3", "v1.2.3\n", "installed", "v1.2.3\n", "already records", "", false, true},
		{"downgrade", "v1.0.0", "v1.2.3\n", "installed", "v1.0.0\n", "updated", "", false, false},
		{"dev", "dev", "v1.0.0\n", "installed", "v1.0.0\n", "version is", "", false, false},
		{"empty", "", "", "installed", "", "version is", "", false, false},
		{"describe", "v1.2.3-5-gabc1234", "v1.0.0\n", "installed", "v1.0.0\n", "not a release tag", "", false, false},
		{"elsewhere", "v1.2.3", "v1.0.0\n", "elsewhere", "v1.0.0\n", "running binary is not", "", false, false},
		{"elsewhere, absent", "v1.2.3", "", "elsewhere", "", "running binary is not", "", false, false},
		{"no installed bin", "v1.2.3", "", "nobin", "", "running binary is not", "", false, false},
		{"symlinked exe", "v1.2.3", "v1.0.0\n", "symlink", "v1.2.3\n", "updated", "", false, false},
		{"write failure", "v1.2.3", "", "installed", "", "", "installed-version", true, false},
		{"executable error", "v1.2.3", "", "error", "", "finding the running binary", "", false, false},
		{"missing exe", "v1.2.3", "v1.0.0\n", "missing", "v1.0.0\n", "not writing", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, fake, logs := newTestConfig(t)
			cfg.Version = tc.version
			vf := rootPath(cfg, versionFilePath)
			other := filepath.Join(t.TempDir(), "other")
			switch tc.runFrom {
			case "installed":
				asInstalledBinary(t, &cfg)
			case "elsewhere":
				asInstalledBinary(t, &cfg)
				mustWrite(t, other, "y", 0o755)
				cfg.Executable = func() (string, error) { return other, nil }
			case "nobin":
				mustWrite(t, other, "y", 0o755)
				cfg.Executable = func() (string, error) { return other, nil }
			case "symlink":
				bin := asInstalledBinary(t, &cfg)
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(bin, link); err != nil {
					t.Fatal(err)
				}
				cfg.Executable = func() (string, error) { return link, nil }
			case "missing":
				asInstalledBinary(t, &cfg)
				cfg.Executable = func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }
			case "error":
				cfg.Executable = func() (string, error) { return "", errors.New("boom") }
			}
			oldMtime := time.Unix(1_000_000_000, 0)
			switch {
			case tc.existingIsDir:
				// A directory in place of the file fails for root and
				// non-root alike, unlike chmod tricks.
				if err := os.MkdirAll(vf, 0o755); err != nil {
					t.Fatal(err)
				}
			case tc.existing != "":
				mustWrite(t, vf, tc.existing, 0o644)
				if err := os.Chtimes(vf, oldMtime, oldMtime); err != nil {
					t.Fatal(err)
				}
			}
			err := Install(context.Background(), cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if got := systemctlCalls(t, fake); len(got) != 0 {
					t.Errorf("systemctl called after failure: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, rerr := os.ReadFile(vf)
			switch {
			case tc.wantFile == "" && rerr == nil:
				t.Errorf("version file written: %q", b)
			case tc.wantFile != "" && string(b) != tc.wantFile:
				t.Errorf("version file = %q (%v), want %q", b, rerr, tc.wantFile)
			}
			if tc.wantFile != "" && tc.existing == "" || tc.wantLog == "updated" {
				if fi, err := os.Stat(vf); err != nil || fi.Mode().Perm() != 0o644 {
					t.Errorf("version file mode: %v, %v", fi, err)
				}
			}
			if tc.wantKeepMtime {
				fi, err := os.Stat(vf)
				if err != nil || !fi.ModTime().Equal(oldMtime) {
					t.Errorf("mtime changed: %v, %v", fi, err)
				}
			}
			if !logContains(*logs, tc.wantLog) {
				t.Errorf("log lacks %q: %v", tc.wantLog, *logs)
			}
			if tc.runFrom == "nobin" && !logContains(*logs, "WARNING") {
				t.Errorf("no binary warning: %v", *logs)
			}
		})
	}
}

func TestInstall_SystemctlFailure(t *testing.T) {
	t.Run("enable fails", func(t *testing.T) {
		cfg, fake, _ := newTestConfig(t)
		fake.Func = func(c cmdrun.Call) ([]byte, error) {
			if strings.Join(c.Args, " ") == "enable gitops-agent" {
				return []byte("boom"), errors.New("exit status 1")
			}
			return nil, nil
		}
		err := Install(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "systemctl enable gitops-agent") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v", err)
		}
		if got := systemctlCalls(t, fake); !equalStrings(got, []string{showStale, "daemon-reload", "enable gitops-agent"}) {
			t.Errorf("calls = %v", got)
		}
		if _, statErr := os.Stat(rootPath(cfg, svcPath)); statErr != nil {
			t.Error("files should remain written")
		}
	})

	t.Run("restart fails", func(t *testing.T) {
		cfg, fake, _ := newTestConfig(t)
		fake.Func = func(c cmdrun.Call) ([]byte, error) {
			switch strings.Join(c.Args, " ") {
			case "is-active --quiet gitops-agent", "restart gitops-agent":
				return nil, errors.New("fail")
			}
			return nil, nil
		}
		if err := Install(context.Background(), cfg); err == nil {
			t.Fatal("expected an error")
		}
		if contains(systemctlCalls(t, fake), "enable --now gitops-agent-update.timer") {
			t.Error("timer was enabled after a failed restart")
		}
	})
}

func TestInstall_ContextCancelled(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Install(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %v", fake.Calls)
	}
	assertRootEmpty(t, cfg)
}

func TestInstall_BinaryWarning(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		cfg, _, logs := newTestConfig(t)
		if err := Install(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if !logContains(*logs, "WARNING") {
			t.Error("no warning for a missing binary")
		}
	})
	t.Run("present", func(t *testing.T) {
		cfg, _, logs := newTestConfig(t)
		mustWrite(t, rootPath(cfg, binPath), "x", 0o755)
		if err := Install(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if logContains(*logs, "WARNING") {
			t.Errorf("unexpected warning: %v", *logs)
		}
	})
}

func TestInstall_RerunAfterFailedReloadRestarts(t *testing.T) {
	cfg, fake, _ := newTestConfig(t)
	active := true
	needReload := false
	failReload := true
	fake.Func = func(c cmdrun.Call) ([]byte, error) {
		switch strings.Join(c.Args, " ") {
		case showStale:
			if needReload {
				return []byte("yes\n"), nil
			}
			return []byte("no\n"), nil
		case "daemon-reload":
			if failReload {
				return nil, errors.New("dbus hiccup")
			}
			needReload = false
		case "restart gitops-agent":
			active = true
		case "is-active --quiet gitops-agent":
			if !active {
				return nil, errors.New("inactive")
			}
		}
		return nil, nil
	}
	// Seed a running install with an old unit.
	mustWrite(t, rootPath(cfg, svcPath), "stale\n", 0o644)
	needReload = true

	if err := Install(context.Background(), cfg); err == nil {
		t.Fatal("run 1: expected the reload failure")
	}
	if got := mustRead(t, rootPath(cfg, svcPath)); got != goldenService(t) {
		t.Fatal("run 1 should have written the unit before failing")
	}

	failReload = false
	fake.Calls = nil
	if err := Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := systemctlCalls(t, fake); !contains(got, "restart gitops-agent") {
		t.Errorf("run 2 calls = %v, want a restart", got)
	}
}

func TestInstall_BinaryChecks(t *testing.T) {
	cases := []struct {
		name    string
		mode    os.FileMode
		uid     int
		wantErr string
	}{
		{"root 0755", 0o755, 0, ""},
		{"root 0775", 0o775, 0, "writable"},
		{"root 0757", 0o757, 0, "writable"},
		{"user owned", 0o755, 1000, "owned by uid 1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, fake, _ := newTestConfig(t)
			cfg.FileUID = func(fs.FileInfo) (int, bool) { return tc.uid, true }
			bin := rootPath(cfg, binPath)
			mustWrite(t, bin, "x", 0o600)
			if err := os.Chmod(bin, tc.mode); err != nil {
				t.Fatal(err)
			}
			err := Install(context.Background(), cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if len(fake.Calls) != 0 {
				t.Errorf("calls = %v, want none", fake.Calls)
			}
			if _, statErr := os.Stat(rootPath(cfg, configPath)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Error("config.toml was written despite the refusal")
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("ab\u00e5cd", 3); got != "ab" {
		t.Errorf("got %q, want a cut before the split rune", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("got %q", got)
	}
}
