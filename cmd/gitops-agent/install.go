package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/oddsund/gitops-agent/internal/installer"
)

func init() {
	subcommands["install"] = runInstall
}

const (
	defaultConfigPath      = "/etc/gitops-agent/config.toml"
	defaultUnitDir         = "/etc/systemd/system"
	defaultServiceName     = "gitops-agent"
	defaultUpdateTimerUnit = "gitops-agent-update.timer"
	defaultClonePath       = "/opt/gitops-agent/repo"
)

// runInstall is the "gitops-agent install" subcommand: write config.toml
// (if absent) and the systemd units, and enable the services. See
// internal/installer for the actual rendering and idempotency logic --
// this just resolves flags/environment into an installer.Config, the same
// way homelab's iac/provision.bash resolved SUDO_USER/getent before this
// moved in-binary.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	repoURL := fs.String("repo-url", "", "git URL of the config repo to sync (required)")
	installUser := fs.String("user", "", "user gitops-agent.service runs as (default: $SUDO_USER, or the current user)")
	sshKeyPath := fs.String("ssh-key-path", "", "SSH key for git auth and sops decryption (default: <user>'s home/.ssh/id_ed25519)")
	clonePath := fs.String("clone-path", defaultClonePath, "where the config repo is cloned to on this host")
	configPath := fs.String("config", defaultConfigPath, "path to write config.toml to, if it doesn't already exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repoURL == "" {
		return fmt.Errorf("-repo-url is required")
	}

	resolvedUser := *installUser
	if resolvedUser == "" {
		resolvedUser = os.Getenv("SUDO_USER")
	}
	if resolvedUser == "" {
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("determining the install user: %w", err)
		}
		resolvedUser = u.Username
	}

	resolvedSSHKeyPath := *sshKeyPath
	if resolvedSSHKeyPath == "" {
		u, err := user.Lookup(resolvedUser)
		if err != nil {
			return fmt.Errorf("looking up home directory for %s: %w", resolvedUser, err)
		}
		resolvedSSHKeyPath = filepath.Join(u.HomeDir, ".ssh", "id_ed25519")
	}

	cfg := installer.Config{
		User:                  resolvedUser,
		RepoURL:               *repoURL,
		SSHKeyPath:            resolvedSSHKeyPath,
		ClonePath:             *clonePath,
		ConfigPath:            *configPath,
		ServiceUnitPath:       filepath.Join(defaultUnitDir, "gitops-agent.service"),
		UpdateServiceUnitPath: filepath.Join(defaultUnitDir, "gitops-agent-update.service"),
		UpdateTimerUnitPath:   filepath.Join(defaultUnitDir, "gitops-agent-update.timer"),
		ServiceName:           defaultServiceName,
		UpdateTimerUnit:       defaultUpdateTimerUnit,
	}

	return installer.Install(cfg)
}
