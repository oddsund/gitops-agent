// Package installer renders gitops-agent's host-local config.toml and
// systemd units from the templates embedded in the binary (see the root
// gitopsagent package) and installs them -- the file-placement half of
// homelab's iac/provision.bash, moved in-binary (see ga-tqa.3). Installing
// systemd units is privileged behaviour a binary that otherwise never
// runs as root doesn't otherwise have; it is reachable only from the
// explicit "gitops-agent install" subcommand, never from the reconcile
// loop.
package installer

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gitopsagent "github.com/oddsund/gitops-agent"
	"github.com/oddsund/gitops-agent/internal/config"
)

// Config supplies the host-specific substitutions and the filesystem
// targets to install to. The *Path/*UnitPath fields point at the real
// /etc locations in cmd/gitops-agent's wiring; tests point them at a temp
// directory instead.
type Config struct {
	User       string // systemd User= for gitops-agent.service
	RepoURL    string // config.toml's [git].repo_url, used only if config.toml doesn't exist yet
	SSHKeyPath string // config.toml's [sops].ssh_key_path, ditto
	ClonePath  string // config.toml's [git].clone_path, ditto

	ConfigPath            string
	ServiceUnitPath       string
	UpdateServiceUnitPath string
	UpdateTimerUnitPath   string

	ServiceName     string // e.g. "gitops-agent"
	UpdateTimerUnit string // e.g. "gitops-agent-update.timer"
}

// Install writes cfg.ConfigPath (only if it doesn't already exist -- an
// existing one is authoritative and never overwritten), then
// gitops-agent.service and the update timer's two units, and enables
// both. gitops-agent.service's ReadWritePaths= is read back from the
// config now on disk, not from cfg.ClonePath, since an existing
// config.toml's clone_path may differ from this call's flags.
//
// gitops-agent.service is restarted only if its rendered content differs
// from what was already on disk, or the service isn't currently active.
// Unlike provision.bash, this never touches the gitops-agent binary
// itself (see internal/selfupdate for that), so there's no "the binary
// changed" signal to key a restart off of -- a changed unit is the
// closest equivalent, and keeps a repeat call a no-op.
func Install(cfg Config) error {
	if err := writeConfigIfAbsent(cfg); err != nil {
		return err
	}

	agentCfg, err := config.Load(cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("reading back %s: %w", cfg.ConfigPath, err)
	}

	if err := installServiceUnit(cfg, agentCfg.Git.ClonePath); err != nil {
		return err
	}
	return installUpdateUnits(cfg)
}

func writeConfigIfAbsent(cfg Config) error {
	if _, err := os.Stat(cfg.ConfigPath); err == nil {
		log.Printf("%s already exists, leaving it as is", cfg.ConfigPath)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", cfg.ConfigPath, err)
	}

	rendered := renderConfigTOML(gitopsagent.ConfigExampleTOML, cfg.RepoURL, cfg.SSHKeyPath, cfg.ClonePath)
	if err := writeFile(cfg.ConfigPath, rendered); err != nil {
		return err
	}
	log.Printf("wrote %s from the template", cfg.ConfigPath)
	return nil
}

func installServiceUnit(cfg Config, clonePath string) error {
	rendered := renderServiceUnit(gitopsagent.ServiceUnitTemplate, cfg.User, clonePath)

	changed, err := writeIfChanged(cfg.ServiceUnitPath, rendered)
	if err != nil {
		return err
	}

	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", cfg.ServiceName); err != nil {
		return err
	}

	active := systemctl("is-active", "--quiet", cfg.ServiceName) == nil
	if changed || !active {
		log.Printf("restarting %s", cfg.ServiceName)
		if err := systemctl("restart", cfg.ServiceName); err != nil {
			return err
		}
	} else {
		log.Printf("%s already enabled and running, no restart needed", cfg.ServiceName)
	}
	return nil
}

// installUpdateUnits writes the update timer's two units straight from
// the embedded source: neither needs a host-specific substitution.
func installUpdateUnits(cfg Config) error {
	if err := writeFile(cfg.UpdateServiceUnitPath, gitopsagent.UpdateServiceUnit); err != nil {
		return err
	}
	if err := writeFile(cfg.UpdateTimerUnitPath, gitopsagent.UpdateTimerUnit); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", cfg.UpdateTimerUnit); err != nil {
		return err
	}
	log.Printf("%s enabled", cfg.UpdateTimerUnit)
	return nil
}

type lineSub struct {
	prefix string
	value  string
}

// substituteLines rewrites each line of tmpl that starts with one of
// subs' prefixes to prefix+value, first match wins. Mirrors provision.
// bash's `sed -e "s/^prefix.*/prefixvalue/"` chains: unmatched lines,
// including every comment, pass through byte-for-byte.
func substituteLines(tmpl string, subs []lineSub) string {
	scanner := bufio.NewScanner(strings.NewReader(tmpl))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		for _, s := range subs {
			if strings.HasPrefix(line, s.prefix) {
				line = s.prefix + s.value
				break
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func renderServiceUnit(tmpl, user, readWritePaths string) string {
	return substituteLines(tmpl, []lineSub{
		{"User=", user},
		{"ReadWritePaths=", readWritePaths},
	})
}

func renderConfigTOML(tmpl, repoURL, sshKeyPath, clonePath string) string {
	return substituteLines(tmpl, []lineSub{
		{"repo_url = ", fmt.Sprintf("%q", repoURL)},
		{"ssh_key_path = ", fmt.Sprintf("%q", sshKeyPath)},
		{"clone_path = ", fmt.Sprintf("%q", clonePath)},
	})
}

// writeIfChanged writes content to path, reporting whether it differs
// from what was there before -- a missing file counts as different.
func writeIfChanged(path, content string) (bool, error) {
	old, err := os.ReadFile(path)
	changed := err != nil || string(old) != content
	if err := writeFile(path, content); err != nil {
		return false, err
	}
	return changed, nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}
