// Package config parses the agent's two configuration files: the
// host-local bootstrap config (where the repo is, how to reach it), and
// the repo-tracked services manifest (what to deploy).
package config

import (
	"fmt"
	"log"

	"github.com/BurntSushi/toml"
)

// AgentConfig is the host-local bootstrap config: where the repo lives and
// how to sync/decrypt it. It's written once by the provisioning script and
// loaded once at startup -- it rarely changes, and doing so is a host
// operation, not a git commit.
type AgentConfig struct {
	Git    Git          `toml:"git"`
	Sops   Sops         `toml:"sops"`
	State  StateConfig  `toml:"state"`
	Status StatusConfig `toml:"status"`
}

type Git struct {
	RepoURL   string `toml:"repo_url"`
	Branch    string `toml:"branch"`
	ClonePath string `toml:"clone_path"`

	// PullIntervalSeconds is the idle cadence: how often to poll when
	// nothing has changed for a while.
	PullIntervalSeconds int `toml:"pull_interval_seconds"`

	// ActiveIntervalSeconds is the cadence used for ActiveWindowSeconds
	// after a poll that found new commits, so a follow-up fix lands in
	// seconds rather than waiting out a full idle interval.
	ActiveIntervalSeconds int `toml:"active_interval_seconds"`
	ActiveWindowSeconds   int `toml:"active_window_seconds"`

	// FullReconcileIntervalSeconds bounds how long drift can go unfixed.
	// Deploys are normally skipped for services whose files didn't change,
	// so without this a container stopped by hand would stay stopped.
	FullReconcileIntervalSeconds int `toml:"full_reconcile_interval_seconds"`
}

// Defaults for the cadence knobs above, applied when they're absent or
// non-positive. Existing host configs predate these keys, so they must keep
// working untouched -- that's the whole point of defaulting rather than
// requiring them.
const (
	DefaultActiveIntervalSeconds        = 15
	DefaultActiveWindowSeconds          = 900
	DefaultFullReconcileIntervalSeconds = 3600
)

type Sops struct {
	SSHKeyPath string `toml:"ssh_key_path"`
}

// StateConfig points at gitops-agent's record of what it last deployed and
// from where (internal/state), used to tear a service down when it's
// disabled or its [[services]] block disappears from services.toml
// entirely. Host-local like the rest of AgentConfig: which services exist
// is repo state, but where this host keeps its own bookkeeping is not.
type StateConfig struct {
	Path string `toml:"path"`
}

// DefaultStatePath is where the state file lives when [state].path is
// absent from config.toml. It matches the systemd unit's
// StateDirectory=gitops-agent, which is what actually creates
// /var/lib/gitops-agent with the right ownership.
const DefaultStatePath = "/var/lib/gitops-agent/deployed.json"

// StatusConfig points at the HTTP status page (see internal/statusserver):
// agent version, last sync, and per-service deploy state, for a page you
// can pull up over the tailnet instead of tailing journald.
type StatusConfig struct {
	ListenAddr string `toml:"listen_addr"`
}

// DefaultStatusListenAddr binds loopback-only by default: this config
// default has to be safe for a bare `go run` or a laptop test run, not just
// the provisioned host. Reaching it from a reverse proxy (which runs in a container)
// needs a listen_addr the docker bridge can route to -- see
// config.example.toml and README.md for how the actual host
// config overrides this.
const DefaultStatusListenAddr = "127.0.0.1:9090"

// ServicesConfig is the desired-state manifest: which services to deploy.
// Unlike AgentConfig, this lives inside the synced repo and is reloaded
// every poll cycle, so enabling a service is a commit, not an SSH session.
type ServicesConfig struct {
	Services []Service `toml:"services"`
}

type Service struct {
	Name    string `toml:"name"`
	Path    string `toml:"path"`
	Enabled bool   `toml:"enabled"`
}

// Load reads and validates the host-local bootstrap config at path.
func Load(path string) (*AgentConfig, error) {
	var cfg AgentConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decoding config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	log.Printf("config: loaded %s", path)
	return &cfg, nil
}

// applyDefaults fills in the cadence knobs a pre-existing config.toml won't
// have. Called before Validate, so validation only ever sees real values.
func (c *AgentConfig) applyDefaults() {
	if c.Git.ActiveIntervalSeconds <= 0 {
		c.Git.ActiveIntervalSeconds = DefaultActiveIntervalSeconds
	}
	if c.Git.ActiveWindowSeconds <= 0 {
		c.Git.ActiveWindowSeconds = DefaultActiveWindowSeconds
	}
	if c.Git.FullReconcileIntervalSeconds <= 0 {
		c.Git.FullReconcileIntervalSeconds = DefaultFullReconcileIntervalSeconds
	}
	if c.State.Path == "" {
		c.State.Path = DefaultStatePath
	}
	if c.Status.ListenAddr == "" {
		c.Status.ListenAddr = DefaultStatusListenAddr
	}
}

func (c *AgentConfig) Validate() error {
	if c.Git.RepoURL == "" {
		return fmt.Errorf("git.repo_url is required")
	}
	if c.Git.Branch == "" {
		return fmt.Errorf("git.branch is required")
	}
	if c.Git.ClonePath == "" {
		return fmt.Errorf("git.clone_path is required")
	}
	if c.Git.PullIntervalSeconds <= 0 {
		return fmt.Errorf("git.pull_interval_seconds must be positive")
	}
	// A "fast" cadence slower than the idle one is always a mistake, and a
	// silently-ignored one would be very annoying to debug.
	if c.Git.ActiveIntervalSeconds > c.Git.PullIntervalSeconds {
		return fmt.Errorf("git.active_interval_seconds (%d) must not exceed git.pull_interval_seconds (%d)",
			c.Git.ActiveIntervalSeconds, c.Git.PullIntervalSeconds)
	}
	if c.Sops.SSHKeyPath == "" {
		return fmt.Errorf("sops.ssh_key_path is required")
	}
	return nil
}

// LoadServices reads and validates the repo-tracked services manifest at
// path. Callers reload this every poll cycle, after syncing the repo.
func LoadServices(path string) (*ServicesConfig, error) {
	var cfg ServicesConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decoding services manifest %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid services manifest %s: %w", path, err)
	}
	log.Printf("config: loaded %s (%d service(s) configured, %d enabled)", path, len(cfg.Services), len(cfg.EnabledServices()))
	return &cfg, nil
}

func (c *ServicesConfig) Validate() error {
	seen := make(map[string]bool, len(c.Services))
	for _, s := range c.Services {
		if s.Name == "" {
			return fmt.Errorf("service with empty name")
		}
		if s.Path == "" {
			return fmt.Errorf("service %q: path is required", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate service name %q", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

// EnabledServices returns the services with enabled = true, in config order.
func (c *ServicesConfig) EnabledServices() []Service {
	var out []Service
	for _, s := range c.Services {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}
