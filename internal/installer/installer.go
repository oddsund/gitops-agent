// Package installer is the file-placement half of homelab's
// iac/provision.bash: it writes config.toml, renders and installs the
// systemd units and enables them.
//
// It is privileged and reachable only from the explicit `gitops-agent
// install` subcommand, never from the reconcile loop. It reads nothing from
// the synced repo: the unit text comes from the copy embedded in the binary.
package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	gitopsagent "github.com/oddsund/gitops-agent"
	"github.com/oddsund/gitops-agent/internal/cmdrun"
	"github.com/oddsund/gitops-agent/internal/config"
)

const (
	serviceName     = "gitops-agent"
	updateTimerName = "gitops-agent-update.timer"

	configPath      = "/etc/gitops-agent/config.toml"
	versionFilePath = "/etc/gitops-agent/installed-version" // same file selfupdate reads
	binPath         = "/usr/local/bin/gitops-agent"
	unitDir         = "/etc/systemd/system"

	serviceUnitName       = "gitops-agent.service"
	updateServiceUnitName = "gitops-agent-update.service"
)

type Config struct {
	User       string // systemd User= for gitops-agent.service. Required.
	RepoURL    string // config.toml [git].repo_url. Required only when config.toml does not exist.
	SSHKeyPath string // config.toml [sops].ssh_key_path. Same.
	ClonePath  string // config.toml [git].clone_path. Same.
	Version    string // main.version; "dev" or "" means do not write the version file.

	// Root is joined in front of every fixed path above. Tests only; there
	// is deliberately no flag or env var for it. Must be absolute if set.
	// Values written into files (ReadWritePaths, config contents) are host
	// paths and are never prefixed.
	Root string
	// Runner defaults to cmdrun.Real.
	Runner cmdrun.Runner
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
	// EUID defaults to os.Geteuid. Replaced in tests to skip the root check.
	EUID func() int
	// FileUID returns the owning uid of a stat result. Defaults to reading
	// syscall.Stat_t. Replaced in tests, which cannot chown under a temp
	// Root.
	FileUID func(fs.FileInfo) (int, bool)
}

// Install is idempotent: unchanged files are not rewritten and the agent
// restarts only when its unit changed (or systemd reports it stale) or it is
// not active. It refuses to run if the installed binary is not root-owned
// or is group/world-writable. There is no
// rollback on failure; files stay written and rerunning is the recovery.
func Install(ctx context.Context, cfg Config) error {
	if cfg.Runner == nil {
		cfg.Runner = cmdrun.Real{}
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.EUID == nil {
		cfg.EUID = os.Geteuid
	}
	if cfg.FileUID == nil {
		cfg.FileUID = fileUID
	}
	if cfg.EUID() != 0 {
		return errors.New("install must run as root")
	}

	if err := validateAll(cfg); err != nil {
		return err
	}

	root := cfg.Root
	if root == "" {
		root = "/"
	}
	p := func(rel string) string { return filepath.Join(root, rel) }
	cfgFile := p(configPath)

	needConfig := false
	if _, err := os.Lstat(cfgFile); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", cfgFile, err)
		}
		needConfig = true
	}
	var rendered string
	if needConfig {
		if cfg.RepoURL == "" || cfg.SSHKeyPath == "" || cfg.ClonePath == "" {
			return fmt.Errorf("%s does not exist yet: RepoURL, SSHKeyPath and ClonePath are required to create it", cfgFile)
		}
		var err error
		rendered, err = renderConfigTOML(gitopsagent.ConfigExampleTOML, cfg.RepoURL, cfg.SSHKeyPath, cfg.ClonePath)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Everything above touches neither disk nor systemd, so a refusal here
	// leaves the host untouched.
	if err := checkBinary(cfg, p(binPath)); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cfgFile), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(cfgFile), err)
	}
	if needConfig {
		created, err := writeNew(cfgFile, rendered, 0o644)
		if err != nil {
			return err
		}
		if created {
			cfg.Logf("wrote %s from the template", cfgFile)
		} else {
			cfg.Logf("%s already exists, leaving it as is", cfgFile)
		}
	} else {
		cfg.Logf("%s already exists, leaving it as is", cfgFile)
	}

	// ReadWritePaths= must match the clone_path actually on disk, not the
	// flag: an existing config.toml is never overwritten, so the two can
	// differ. A hand-edited config could also carry a line break through a
	// TOML escape, so the value is revalidated before it reaches the unit.
	agentCfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("reading back %s: %w", cfgFile, err)
	}
	rw := agentCfg.Git.ClonePath
	if err := validateUnitPath("clone_path in "+cfgFile, rw); err != nil {
		return err
	}
	svc, err := renderServiceUnit(gitopsagent.ServiceUnitTemplate, cfg.User, rw)
	if err != nil {
		return err
	}

	svcRes, err := writeUnit(cfg, p(filepath.Join(unitDir, serviceUnitName)), svc)
	if err != nil {
		return err
	}
	if _, err := writeUnit(cfg, p(filepath.Join(unitDir, updateServiceUnitName)), gitopsagent.UpdateServiceUnit); err != nil {
		return err
	}
	timerRes, err := writeUnit(cfg, p(filepath.Join(unitDir, updateTimerName)), gitopsagent.UpdateTimerUnit)
	if err != nil {
		return err
	}

	if err := installVersionFile(cfg, p(versionFilePath)); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	systemctl := func(args ...string) error {
		out, err := cfg.Runner.Run(ctx, nil, "systemctl", args...)
		if err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, truncate(strings.TrimSpace(string(out)), 1024))
		}
		return nil
	}

	// An earlier run may have written the unit and then failed before the
	// restart; the bytes now match, so only systemd knows the loaded unit
	// is stale. Read it before the reload clears the flag. An error (unit
	// not known yet) means no.
	out, _ := cfg.Runner.Run(ctx, nil, "systemctl", "show", "-p", "NeedDaemonReload", "--value", serviceName+".service")
	staleUnit := strings.TrimSpace(string(out)) == "yes"

	// Every time, not only after a change: it heals an earlier run that
	// wrote units and died before reloading. It is not a restart.
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", serviceName); err != nil {
		return err
	}
	// Any error counts as inactive; a broken systemctl fails the next step.
	active := systemctl("is-active", "--quiet", serviceName) == nil
	if svcRes.Changed || staleUnit || !active {
		cfg.Logf("restarting %s", serviceName)
		if err := systemctl("restart", serviceName); err != nil {
			return err
		}
	} else {
		cfg.Logf("%s already enabled and running, no restart needed", serviceName)
	}
	if err := systemctl("enable", "--now", updateTimerName); err != nil {
		return err
	}
	// enable --now does not reload an already active timer, so a changed
	// OnCalendar= would otherwise not take effect.
	if timerRes.Changed && !timerRes.Created {
		if err := systemctl("restart", updateTimerName); err != nil {
			return err
		}
	}
	cfg.Logf("%s enabled", updateTimerName)
	cfg.Logf("install complete")
	return nil
}

func validateAll(cfg Config) error {
	if err := validateRoot(cfg.Root); err != nil {
		return err
	}
	if err := validateUser(cfg.User); err != nil {
		return fmt.Errorf("User: %w", err)
	}
	if cfg.RepoURL != "" {
		if err := validateRepoURL(cfg.RepoURL); err != nil {
			return fmt.Errorf("RepoURL: %w", err)
		}
	}
	if cfg.SSHKeyPath != "" {
		if err := validateSSHKeyPath(cfg.SSHKeyPath); err != nil {
			return fmt.Errorf("SSHKeyPath: %w", err)
		}
	}
	if cfg.ClonePath != "" {
		if err := validateUnitPath("clone path", cfg.ClonePath); err != nil {
			return fmt.Errorf("ClonePath: %w", err)
		}
	}
	return nil
}

func writeUnit(cfg Config, path, content string) (writeResult, error) {
	res, err := writeAtomic(path, content, 0o644)
	if err != nil {
		return res, err
	}
	if res.Changed {
		cfg.Logf("wrote %s", path)
	} else {
		cfg.Logf("%s unchanged", path)
	}
	return res, nil
}

// installVersionFile writes the file only when it is absent and the build
// is a release tag. After the first write `gitops-agent update` owns it,
// and the binary running install may be older or newer than what it
// records. A `git describe` build must not poison the file: update treats
// an unparsable value as "update available".
func installVersionFile(cfg Config, path string) error {
	v := cfg.Version
	if v == "" || v == "dev" {
		cfg.Logf("not writing %s: version is %q", path, v)
		return nil
	}
	if !versionRE.MatchString(v) {
		cfg.Logf("not writing %s: version %q is not a release tag (vX.Y.Z)", path, v)
		return nil
	}
	created, err := writeNew(path, v+"\n", 0o644)
	if err != nil {
		return err
	}
	if created {
		cfg.Logf("wrote %s (%s)", path, v)
	} else {
		cfg.Logf("%s already exists, leaving it as is", path)
	}
	return nil
}

// checkBinary refuses a binary the unprivileged service user could replace:
// gitops-agent-update.service executes it as root. A missing binary is only
// a warning because it matters at service start, not at install time.
func checkBinary(cfg Config, path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg.Logf("WARNING: %s not found; %s will fail to start until the binary is installed", binPath, serviceName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s: %w", binPath, err)
	}
	if uid, ok := cfg.FileUID(fi); !ok {
		return fmt.Errorf("cannot determine the owner of %s", binPath)
	} else if uid != 0 {
		return fmt.Errorf("refusing to install: %s is owned by uid %d, not root; the update timer runs it as root", binPath, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("refusing to install: %s is group- or world-writable (mode %04o); the update timer runs it as root", binPath, fi.Mode().Perm())
	}
	return nil
}

func fileUID(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
