package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/oddsund/gitops-agent/internal/selfupdate"
)

func init() {
	subcommands["update"] = runUpdate
}

// runUpdate is the "gitops-agent update" subcommand: check GitHub for a
// newer release and, if there is one, install and restart it (see
// internal/selfupdate). Configuration is env vars, not flags, matching
// systemd/update.bash's own interface -- gitops-agent-update.service
// already sets these, and keeping the names lets a host move to this
// subcommand with no unit changes beyond ExecStart.
func runUpdate(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("update takes no arguments")
	}

	token := ""
	tokenFile := envOr("GITHUB_TOKEN_FILE", "/etc/gitops-agent/github-token")
	if data, err := os.ReadFile(tokenFile); err == nil {
		token = strings.TrimSpace(string(data))
	}

	cfg := selfupdate.Config{
		Repo:            envOr("GITOPS_AGENT_REPO", "oddsund/gitops-agent"),
		Token:           token,
		BinPath:         envOr("GITOPS_AGENT_BIN", "/usr/local/bin/gitops-agent"),
		VersionFilePath: envOr("GITOPS_AGENT_VERSION_FILE", "/etc/gitops-agent/installed-version"),
		ServiceName:     envOr("GITOPS_AGENT_SERVICE", "gitops-agent"),
		SkipAttestation: os.Getenv("GITOPS_AGENT_SKIP_ATTESTATION") == "1",
	}

	return selfupdate.Update(cfg)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
