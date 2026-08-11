// Package config parses the agent's single TOML configuration file.
package config

import (
	"fmt"
	"log"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Git      Git       `toml:"git"`
	Sops     Sops      `toml:"sops"`
	Services []Service `toml:"services"`
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

type Service struct {
	Name    string `toml:"name"`
	Path    string `toml:"path"`
	Enabled bool   `toml:"enabled"`
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decoding config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	log.Printf("config: loaded %s (%d service(s) configured, %d enabled)", path, len(cfg.Services), len(cfg.EnabledServices()))
	return &cfg, nil
}

func (c *Config) Validate() error {
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
func (c *Config) EnabledServices() []Service {
	var out []Service
	for _, s := range c.Services {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}
