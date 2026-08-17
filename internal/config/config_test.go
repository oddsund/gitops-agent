package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load("testdata/valid.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Git.RepoURL != "git@github.com:yourname/your-gitops-config.git" {
		t.Errorf("RepoURL = %q", cfg.Git.RepoURL)
	}
	if cfg.Git.Branch != "main" {
		t.Errorf("Branch = %q", cfg.Git.Branch)
	}
	if cfg.Git.ClonePath != "/opt/gitops/repo" {
		t.Errorf("ClonePath = %q", cfg.Git.ClonePath)
	}
	if cfg.Git.PullIntervalSeconds != 300 {
		t.Errorf("PullIntervalSeconds = %d", cfg.Git.PullIntervalSeconds)
	}
	if cfg.Sops.SSHKeyPath != "/home/pi/.ssh/id_ed25519" {
		t.Errorf("SSHKeyPath = %q", cfg.Sops.SSHKeyPath)
	}
}

func TestLoad_MissingRequiredField(t *testing.T) {
	if _, err := Load("testdata/missing_repo_url.toml"); err == nil {
		t.Fatal("expected error for missing git.repo_url, got nil")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load("testdata/does_not_exist.toml"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadServices_Valid(t *testing.T) {
	cfg, err := LoadServices("testdata/services_valid.toml")
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("len(Services) = %d, want 2", len(cfg.Services))
	}

	enabled := cfg.EnabledServices()
	if len(enabled) != 1 || enabled[0].Name != "demoapp" {
		t.Errorf("EnabledServices() = %+v, want just demoapp", enabled)
	}
}

func TestLoadServices_MissingFile(t *testing.T) {
	if _, err := LoadServices("testdata/does_not_exist.toml"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadServices_DuplicateName(t *testing.T) {
	if _, err := LoadServices("testdata/services_duplicate_name.toml"); err == nil {
		t.Fatal("expected error for duplicate service name, got nil")
	}
}

func TestValidate_DuplicateServiceName(t *testing.T) {
	cfg := ServicesConfig{
		Services: []Service{
			{Name: "a", Path: "a", Enabled: true},
			{Name: "a", Path: "b", Enabled: false},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate service name, got nil")
	}
}

func TestLoad_CadenceDefaultsWhenAbsent(t *testing.T) {
	// valid.toml predates the cadence keys, like every config.toml already
	// sitting on a host. Those must keep loading.
	cfg, err := Load("testdata/valid.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Git.ActiveIntervalSeconds != DefaultActiveIntervalSeconds {
		t.Errorf("ActiveIntervalSeconds = %d, want the default %d", cfg.Git.ActiveIntervalSeconds, DefaultActiveIntervalSeconds)
	}
	if cfg.Git.ActiveWindowSeconds != DefaultActiveWindowSeconds {
		t.Errorf("ActiveWindowSeconds = %d, want the default %d", cfg.Git.ActiveWindowSeconds, DefaultActiveWindowSeconds)
	}
	if cfg.Git.FullReconcileIntervalSeconds != DefaultFullReconcileIntervalSeconds {
		t.Errorf("FullReconcileIntervalSeconds = %d, want the default %d", cfg.Git.FullReconcileIntervalSeconds, DefaultFullReconcileIntervalSeconds)
	}
}

func TestLoad_CadenceFromFile(t *testing.T) {
	cfg, err := Load("testdata/valid_cadence.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Git.ActiveIntervalSeconds != 5 {
		t.Errorf("ActiveIntervalSeconds = %d, want 5", cfg.Git.ActiveIntervalSeconds)
	}
	if cfg.Git.ActiveWindowSeconds != 120 {
		t.Errorf("ActiveWindowSeconds = %d, want 120", cfg.Git.ActiveWindowSeconds)
	}
	if cfg.Git.FullReconcileIntervalSeconds != 600 {
		t.Errorf("FullReconcileIntervalSeconds = %d, want 600", cfg.Git.FullReconcileIntervalSeconds)
	}
}

func TestLoad_ActiveIntervalSlowerThanIdleIsRejected(t *testing.T) {
	if _, err := Load("testdata/active_interval_too_slow.toml"); err == nil {
		t.Fatal("expected error when active_interval_seconds exceeds pull_interval_seconds, got nil")
	}
}

// TestRepoServicesManifest_PathsExist guards against the failure mode
// services.toml warns about in its own header comment: a bad path is a
// silent no-op at runtime, since the agent falls back to the last-known-good
// list instead of failing loudly. CI is the only place left to catch it.
func TestRepoServicesManifest_PathsExist(t *testing.T) {
	const manifest = "../../../services.toml"
	// Only meaningful from a checkout that also holds the manifest this
	// agent deploys from; there is nothing to check otherwise.
	if _, err := os.Stat(manifest); err != nil {
		t.Skipf("no manifest at %s: nothing to check", manifest)
	}

	cfg, err := LoadServices(manifest)
	if err != nil {
		t.Fatalf("LoadServices: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Fatal("services.toml declares no services")
	}

	for _, s := range cfg.Services {
		info, err := os.Stat(filepath.Join("../../..", s.Path))
		if err != nil {
			t.Errorf("service %q: path %q: %v", s.Name, s.Path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("service %q: path %q is not a directory", s.Name, s.Path)
			continue
		}
		composePath := filepath.Join("../../..", s.Path, "compose.yml")
		if _, err := os.Stat(composePath); err != nil {
			t.Errorf("service %q: %v", s.Name, err)
		}
	}
}
