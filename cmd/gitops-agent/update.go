package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/oddsund/gitops-agent/internal/selfupdate"
)

func init() { subcommands["update"] = runUpdate }

func runUpdate(args []string) error {
	cfg, err := newUpdateConfig(args, os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	// Checked before the token file is read, so an unprivileged user gets
	// this message rather than a permission error on the token.
	if os.Geteuid() != 0 {
		return errors.New("update must run as root")
	}
	cfg.Token, err = readToken(os.Getenv, os.ReadFile)
	if err != nil {
		return err
	}
	// journald adds the timestamps.
	cfg.Logf = log.New(os.Stderr, "gitops-agent update: ", 0).Printf

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return selfupdate.Update(ctx, cfg)
}

// newUpdateConfig applies flag > environment > built-in default. The
// environment variables are the ones systemd/update.bash already reads, so
// the existing unit environment carries over. An empty value counts as unset.
// Parse errors are returned, not printed, so dispatch reports them once.
func newUpdateConfig(args []string, getenv func(string) string, stderr io.Writer) (selfupdate.Config, error) {
	flagSet := flag.NewFlagSet("update", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.Usage = func() {}

	force := flagSet.Bool("force", false, "install the latest release even if it is not newer")
	bin := flagSet.String("bin", cmp.Or(getenv("GITOPS_AGENT_BIN"), "/usr/local/bin/gitops-agent"), "path of the binary to replace (env GITOPS_AGENT_BIN)")
	versionFile := flagSet.String("version-file", cmp.Or(getenv("GITOPS_AGENT_VERSION_FILE"), "/etc/gitops-agent/installed-version"), "file recording the installed version (env GITOPS_AGENT_VERSION_FILE)")
	service := flagSet.String("service", cmp.Or(getenv("GITOPS_AGENT_SERVICE"), "gitops-agent"), "systemd service to restart (env GITOPS_AGENT_SERVICE)")

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "usage: gitops-agent update [--force] [--bin PATH] [--version-file PATH] [--service NAME]")
			flagSet.SetOutput(stderr)
			flagSet.PrintDefaults()
		}
		return selfupdate.Config{}, err
	}
	if flagSet.NArg() > 0 {
		return selfupdate.Config{}, errors.New("update takes no arguments")
	}

	return selfupdate.Config{
		Repo:            cmp.Or(getenv("GITOPS_AGENT_REPO"), "oddsund/gitops-agent"),
		BinPath:         *bin,
		VersionFilePath: *versionFile,
		ServiceName:     *service,
		SkipAttestation: getenv("GITOPS_AGENT_SKIP_ATTESTATION") == "1",
		Force:           *force,
	}, nil
}

// readToken returns "" when the token file does not exist: the token only
// raises API rate limits.
func readToken(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	tokenFile := cmp.Or(getenv("GITHUB_TOKEN_FILE"), "/etc/gitops-agent/github-token")
	b, err := readFile(tokenFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading token file %s: %w", tokenFile, err)
	}
	return strings.TrimSpace(string(b)), nil
}
