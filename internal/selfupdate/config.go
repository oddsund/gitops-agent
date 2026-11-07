package selfupdate

import (
	"cmp"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/oddsund/gitops-agent/internal/cmdrun"
)

type Config struct {
	// Empty defaults to production github
	APIBaseURL string
	// Empty defaults to gitops-agent-linux-<GOARCH>, resolved when the asset
	// is fetched.
	AssetName string
	// Defaults to github.com
	DownloadBaseURL string
	// Empty defaults to "oddsund/gitops-agent"
	Repo string
	// Empty defaults to no token. Sent only to the release API request, to
	// avoid rate limits; never to downloads.
	Token string
	// Empty defaults to a client with a five minute timeout.
	HTTPClient *http.Client

	// Empty defaults to /usr/local/bin/gitops-agent. Must be absolute.
	BinPath string
	// Empty defaults to /etc/gitops-agent/installed-version. Must be absolute.
	VersionFilePath string
	// Empty defaults to gitops-agent.
	ServiceName string
	// Nil defaults to cmdrun.Real.
	Runner cmdrun.Runner
	// Nil defaults to log.Printf.
	Logf func(format string, args ...any)
	// Nil defaults to exec.LookPath.
	LookPath func(file string) (string, error)
	// Nil defaults to os.Geteuid. Replaced in tests to skip the root check.
	EUID func() int

	// SkipAttestation limits verification to the checksum.
	SkipAttestation bool
	// Force installs the latest release even if it is not newer.
	Force bool
}

func (cfg Config) populateWithDefaultValues() Config {
	cfg.APIBaseURL = cmp.Or(cfg.APIBaseURL, "https://api.github.com")
	cfg.DownloadBaseURL = cmp.Or(cfg.DownloadBaseURL, "https://github.com")
	cfg.Repo = cmp.Or(cfg.Repo, "oddsund/gitops-agent")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	cfg.BinPath = cmp.Or(cfg.BinPath, "/usr/local/bin/gitops-agent")
	cfg.VersionFilePath = cmp.Or(cfg.VersionFilePath, "/etc/gitops-agent/installed-version")
	cfg.ServiceName = cmp.Or(cfg.ServiceName, "gitops-agent")
	if cfg.Runner == nil {
		cfg.Runner = cmdrun.Real{}
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	if cfg.EUID == nil {
		cfg.EUID = os.Geteuid
	}
	return cfg
}

// assetNameFor is separate from populateWithDefaultValues because an
// unsupported GOARCH must only fail callers that actually need the asset.
func assetNameFor(override, goarch string) (string, error) {
	if override != "" {
		if err := validateAssetName(override); err != nil {
			return "", err
		}
		return override, nil
	}
	switch goarch {
	case "arm64", "amd64":
		return "gitops-agent-linux-" + goarch, nil
	}
	return "", fmt.Errorf("no release asset published for GOARCH %q (set an explicit asset name)", goarch)
}
