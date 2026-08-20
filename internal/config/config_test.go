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

func TestLoad_StatePathDefaultWhenAbsent(t *testing.T) {
	cfg, err := Load("testdata/valid.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State.Path != DefaultStatePath {
		t.Errorf("State.Path = %q, want default %q", cfg.State.Path, DefaultStatePath)
	}
}

func TestLoad_StatePathFromFile(t *testing.T) {
	cfg, err := Load("testdata/valid_state_path.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "/tmp/gitops-agent-test/deployed.json"; cfg.State.Path != want {
		t.Errorf("State.Path = %q, want %q", cfg.State.Path, want)
	}
}

func TestLoad_StatusListenAddrDefaultWhenAbsent(t *testing.T) {
	cfg, err := Load("testdata/valid.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Status.ListenAddr != DefaultStatusListenAddr {
		t.Errorf("Status.ListenAddr = %q, want default %q", cfg.Status.ListenAddr, DefaultStatusListenAddr)
	}
}

func TestLoad_StatusListenAddrFromFile(t *testing.T) {
	cfg, err := Load("testdata/valid_status.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "0.0.0.0:9090"; cfg.Status.ListenAddr != want {
		t.Errorf("Status.ListenAddr = %q, want %q", cfg.Status.ListenAddr, want)
	}
}

func TestLoad_ActiveIntervalSlowerThanIdleIsRejected(t *testing.T) {
	if _, err := Load("testdata/active_interval_too_slow.toml"); err == nil {
		t.Fatal("expected error when active_interval_seconds exceeds pull_interval_seconds, got nil")
	}
}
