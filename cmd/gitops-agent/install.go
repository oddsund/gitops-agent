package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"

	"github.com/oddsund/gitops-agent/internal/installer"
)

func init() { subcommands["install"] = runInstall }

func runInstall(args []string) error {
	cfg, err := newInstallConfig(args, os.Getenv, user.Lookup, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	// journald adds the timestamps.
	cfg.Logf = log.New(os.Stderr, "gitops-agent install: ", 0).Printf

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return installer.Install(ctx, cfg)
}

// newInstallConfig only resolves defaults. installer.Install owns
// validation so there is one set of rules.
func newInstallConfig(args []string, getenv func(string) string, lookupUser func(name string) (*user.User, error), stderr io.Writer) (installer.Config, error) {
	flagSet := flag.NewFlagSet("install", flag.ContinueOnError)
	flagSet.SetOutput(stderr)
	flagSet.Usage = func() {
		fmt.Fprintln(stderr, "usage: gitops-agent install --repo-url URL [--user NAME] [--ssh-key PATH] [--clone-path PATH]")
		flagSet.PrintDefaults()
	}

	repoURL := flagSet.String("repo-url", "", "git URL of the config repo; required when /etc/gitops-agent/config.toml does not exist")
	userFlag := flagSet.String("user", "", "user gitops-agent.service runs as (default $SUDO_USER)")
	sshKey := flagSet.String("ssh-key", "", "SSH key for git auth and sops (default ~/.ssh/id_ed25519 of --user, when its home is absolute)")
	clonePath := flagSet.String("clone-path", "/opt/gitops-agent/repo", "where the repo is cloned")

	if err := flagSet.Parse(args); err != nil {
		return installer.Config{}, err
	}
	if flagSet.NArg() > 0 {
		return installer.Config{}, errors.New("install takes no arguments")
	}

	// No fallback to the current user: install requires root, so that
	// would silently be root.
	installUser := cmp.Or(*userFlag, getenv("SUDO_USER"))
	if installUser == "" {
		return installer.Config{}, errors.New("no install user: pass --user (or run via sudo so SUDO_USER is set)")
	}
	// Looked up even with --ssh-key given: a service for a nonexistent
	// user cannot start.
	u, err := lookupUser(installUser)
	if err != nil {
		return installer.Config{}, fmt.Errorf("looking up user %q: %w", installUser, err)
	}

	// A relative or empty home would yield a relative key path that fails
	// validation even when config.toml exists and the value is unused. Leave
	// it empty instead: Install then asks for --ssh-key only if it is needed.
	keyPath := *sshKey
	if keyPath == "" && filepath.IsAbs(u.HomeDir) {
		keyPath = filepath.Join(u.HomeDir, ".ssh", "id_ed25519")
	}

	return installer.Config{
		User:       installUser,
		RepoURL:    *repoURL,
		SSHKeyPath: keyPath,
		ClonePath:  *clonePath,
		Version:    version,
	}, nil
}
