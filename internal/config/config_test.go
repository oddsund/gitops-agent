package config

import "testing"

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
