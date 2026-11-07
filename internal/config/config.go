// Package config parses the agent's two configuration files: the
// host-local bootstrap config (where the repo is, how to reach it), and
// the repo-tracked services manifest (what to deploy).
package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

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
	Notify NotifyConfig `toml:"notify"`
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
// can open from another machine instead of tailing journald.
type StatusConfig struct {
	ListenAddr string `toml:"listen_addr"`
}

// DefaultStatusListenAddr binds loopback-only by default: this config
// default has to be safe for a bare `go run` or a laptop test run, not just
// the provisioned host. Reaching it from a reverse proxy that runs in a
// container needs a listen_addr the docker bridge can route to -- see
// config.example.toml and docs/configuration.md for how a host config
// overrides this.
const DefaultStatusListenAddr = "127.0.0.1:9090"

// NotifyConfig is the optional [notify] section: plain email when a service
// starts failing or recovers. Host-local like everything in AgentConfig, so
// the repository can never choose where mail goes. An empty SMTPHost
// disables notifications and skips all other validation.
//
// There is deliberately no password key: config.toml is world-readable, so
// the password lives in PasswordFile, which internal/notify reads at start.
type NotifyConfig struct {
	SMTPHost           string   `toml:"smtp_host"`
	SMTPPort           int      `toml:"smtp_port"`
	TLS                string   `toml:"tls"`
	From               string   `toml:"from"`
	To                 []string `toml:"to"`
	Username           string   `toml:"username"`
	PasswordFile       string   `toml:"password_file"`
	MinIntervalSeconds int      `toml:"min_interval_seconds"`
	IncludeError       bool     `toml:"include_error"`
	StatusURL          string   `toml:"status_url"`
}

const (
	DefaultNotifyTLS                = "starttls"
	DefaultNotifyMinIntervalSeconds = 3600
	maxNotifyRecipients             = 10
	maxNotifyStatusURL              = 2048
)

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
	c.Notify.applyDefaults()
}

// applyDefaults does nothing while notifications are off, so a config
// without [notify] is left exactly as it was.
func (n *NotifyConfig) applyDefaults() {
	if n.SMTPHost == "" {
		return
	}
	if n.TLS == "" {
		n.TLS = DefaultNotifyTLS
	}
	if n.SMTPPort == 0 {
		switch n.TLS {
		case "implicit":
			n.SMTPPort = 465
		case "none":
			n.SMTPPort = 25
		default:
			n.SMTPPort = 587
		}
	}
	if n.MinIntervalSeconds == 0 {
		n.MinIntervalSeconds = DefaultNotifyMinIntervalSeconds
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
	return c.Notify.validate()
}

// validate is pure: no file reads, no network. config.Load is also called
// by the installer, which must not be affected by host state such as the
// password file.
func (n *NotifyConfig) validate() error {
	if n.SMTPHost == "" {
		return nil
	}
	host := n.SMTPHost
	if strings.ContainsAny(host, " \t\r\n/@") || strings.IndexFunc(host, unicode.IsControl) >= 0 ||
		(strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return fmt.Errorf("notify.smtp_host must be a bare host name or IP address; put the port in notify.smtp_port")
	}
	switch n.TLS {
	case "starttls", "implicit", "none":
	default:
		return fmt.Errorf("notify.tls must be \"starttls\", \"implicit\" or \"none\"")
	}
	if n.SMTPPort < 1 || n.SMTPPort > 65535 {
		return fmt.Errorf("notify.smtp_port must be between 1 and 65535")
	}
	if n.TLS == "none" {
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("notify.tls = \"none\" is only allowed for a loopback smtp_host (a local relay); use \"starttls\" or \"implicit\" for anything else")
		}
		if n.Username != "" {
			return fmt.Errorf("notify.username requires tls = \"starttls\" or \"implicit\": the agent never sends a password in clear text")
		}
	}
	if (n.Username == "") != (n.PasswordFile == "") {
		return fmt.Errorf("notify.username and notify.password_file must be set together")
	}
	if strings.IndexFunc(n.Username, unicode.IsControl) >= 0 {
		return fmt.Errorf("notify.username must not contain control characters")
	}
	if n.PasswordFile != "" && !filepath.IsAbs(n.PasswordFile) {
		return fmt.Errorf("notify.password_file must be an absolute path")
	}
	if _, err := mail.ParseAddress(n.From); err != nil {
		return fmt.Errorf("notify.from: not a valid address")
	}
	if len(n.To) == 0 {
		return fmt.Errorf("notify.to must list at least one address")
	}
	if len(n.To) > maxNotifyRecipients {
		return fmt.Errorf("notify.to has %d addresses; the limit is %d", len(n.To), maxNotifyRecipients)
	}
	for i, a := range n.To {
		if _, err := mail.ParseAddress(a); err != nil {
			return fmt.Errorf("notify.to[%d]: not a valid address", i)
		}
	}
	if n.MinIntervalSeconds < 0 {
		return fmt.Errorf("notify.min_interval_seconds must not be negative")
	}
	return n.validateStatusURL()
}

func (n *NotifyConfig) validateStatusURL() error {
	if n.StatusURL == "" {
		return nil
	}
	if len(n.StatusURL) > maxNotifyStatusURL || strings.IndexFunc(n.StatusURL, unicode.IsControl) >= 0 {
		return fmt.Errorf("notify.status_url is too long or contains control characters")
	}
	u, err := url.Parse(n.StatusURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("notify.status_url must be an http or https URL")
	}
	if u.User != nil {
		return fmt.Errorf("notify.status_url must not contain a user name or password")
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
	return &cfg, nil
}

// serviceNameRE: the name becomes a directory under the secrets tmpfs and
// a key in the state file, so it must be a single safe path segment.
var serviceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// composeProjectName mirrors how docker compose names the project when
// the agent passes -f <dir>/compose.yml and no -p (internal/deploy does
// neither): the directory basename, lowercased, with every character
// outside [a-z0-9_-] dropped, then leading '_' and '-' trimmed (compose-go's
// NormalizeProjectName). This assumes no name:, COMPOSE_PROJECT_NAME or -p
// override: the agent derives the name from the directory alone. Two services that map to the same name
// share containers, networks and volumes and tear each other down.
func composeProjectName(cleanPath string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(cleanPath)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "_-")
}

// Validate checks every service and, on success, rewrites each Path to its
// cleaned form so that Join, gitsync.PathChanged and the status page all see
// one spelling. Paths are not normalised if validation fails. The path check
// is lexical: it does not resolve symlinks.
func (c *ServicesConfig) Validate() error {
	names := make(map[string]bool, len(c.Services))
	paths := make(map[string]string, len(c.Services))
	projects := make(map[string]string, len(c.Services))
	for _, s := range c.Services {
		if s.Name == "" {
			return fmt.Errorf("service with empty name")
		}
		if !serviceNameRE.MatchString(s.Name) {
			return fmt.Errorf("service %q: name must be 1-64 characters, start with a letter or digit, and contain only letters, digits, '.', '_' and '-'", s.Name)
		}
		if s.Path == "" {
			return fmt.Errorf("service %q: path is required", s.Name)
		}
		if strings.ContainsFunc(s.Path, unicode.IsControl) || !filepath.IsLocal(s.Path) {
			return fmt.Errorf("service %q: path %q must be relative and stay inside the repository", s.Name, s.Path)
		}
		clean := filepath.Clean(s.Path)
		if clean == "." {
			return fmt.Errorf("service %q: path %q must name a directory below the repository root", s.Name, s.Path)
		}
		if names[s.Name] {
			return fmt.Errorf("duplicate service name %q", s.Name)
		}
		if owner, ok := paths[clean]; ok {
			return fmt.Errorf("service %q: path %q duplicates the path of service %q", s.Name, s.Path, owner)
		}
		project := composeProjectName(clean)
		if project == "" {
			return fmt.Errorf("service %q: path %q: the directory name gives an empty docker compose project name", s.Name, s.Path)
		}
		if owner, ok := projects[project]; ok {
			return fmt.Errorf("service %q: path %q gives docker compose project name %q, which service %q already uses", s.Name, s.Path, project, owner)
		}
		names[s.Name] = true
		paths[clean] = s.Name
		projects[project] = s.Name
	}
	for i := range c.Services {
		c.Services[i].Path = filepath.Clean(c.Services[i].Path)
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
