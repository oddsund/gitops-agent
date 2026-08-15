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
	Git  Git  `toml:"git"`
	Sops Sops `toml:"sops"`
}

type Git struct {
	RepoURL             string `toml:"repo_url"`
	Branch              string `toml:"branch"`
	ClonePath           string `toml:"clone_path"`
	PullIntervalSeconds int    `toml:"pull_interval_seconds"`
}

type Sops struct {
	SSHKeyPath string `toml:"ssh_key_path"`
}

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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	log.Printf("config: loaded %s", path)
	return &cfg, nil
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
