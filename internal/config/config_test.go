package config

import (
	"strconv"
	"strings"
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

func TestLoadServices_Rejected(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"services_escaping_path.toml", []string{`"demoapp"`, "must be relative and stay inside"}},
		{"services_absolute_path.toml", []string{`"demoapp"`, "must be relative and stay inside"}},
		{"services_duplicate_path.toml", []string{`"other"`, `duplicates the path of service "demoapp"`}},
		{"services_bad_name.toml", []string{`"../evil"`, "name must be"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			_, err := LoadServices("testdata/" + tt.file)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestServicesValidate(t *testing.T) {
	const inside = "must be relative and stay inside"
	const root = "must name a directory below the repository root"
	const badName = "name must be"
	type sv = [2]string // name, path
	tests := []struct {
		name      string
		services  []sv
		wantErr   string
		wantPaths []string
		wantSvc   string // service the error must cite
	}{
		{"plain valid", []sv{{"demoapp", "apps/demo"}}, "", []string{"apps/demo"}, ""},
		{"two valid", []sv{{"a", "a"}, {"b", "b"}}, "", []string{"a", "b"}, ""},
		{"cleaned on success", []sv{{"a", "./apps//demo/"}}, "", []string{"apps/demo"}, ""},
		{"inner dotdot staying inside", []sv{{"a", "x/../y"}}, "", []string{"y"}, ""},
		{"nested paths allowed", []sv{{"a", "a"}, {"b", "a/b"}}, "", []string{"a", "a/b"}, ""},
		{"dotted/dashed name", []sv{{"my-app.v2_x", "svc"}}, "", []string{"svc"}, ""},
		{"name with dots inside", []sv{{"a..b", "svc"}}, "", []string{"svc"}, ""},
		{"64-char name", []sv{{strings.Repeat("a", 64), "svc"}}, "", []string{"svc"}, ""},
		{"control char in path", []sv{{"a", "x\x00y"}}, inside, nil, "a"},
		{"newline in path", []sv{{"a", "x\ny"}}, inside, nil, "a"},
		{"same basename", []sv{{"a", "a/app"}, {"b", "b/app"}}, `service "b": path "b/app" gives docker compose project name "app", which service "a" already uses`, nil, "b"},
		{"basename differs by case", []sv{{"a", "a/App"}, {"b", "b/app"}}, `project name "app"`, nil, "b"},
		{"basename punctuation dropped", []sv{{"a", "a/my.app"}, {"b", "b/myapp"}}, `project name "myapp"`, nil, "b"},
		{"leading underscore trimmed", []sv{{"a", "a/_app"}, {"b", "b/app"}}, `project name "app"`, nil, "b"},
		{"leading dash trimmed", []sv{{"a", "a/-app"}, {"b", "b/app"}}, `project name "app"`, nil, "b"},
		{"empty project name underscores", []sv{{"a", "a/___"}}, "empty docker compose project name", nil, "a"},
		{"empty project name non-ASCII", []sv{{"a", "a/日本"}}, "empty docker compose project name", nil, "a"},
		{"empty name", []sv{{"", "svc"}}, "service with empty name", nil, ""},
		{"empty path", []sv{{"a", ""}}, `service "a": path is required`, nil, "a"},
		{"path ..", []sv{{"a", ".."}}, `service "a": path ".." must be relative`, nil, "a"},
		{"path ../x", []sv{{"a", "../x"}}, inside, nil, "a"},
		{"path /etc", []sv{{"a", "/etc"}}, inside, nil, "a"},
		{"path a/../../b", []sv{{"a", "a/../../b"}}, inside, nil, "a"},
		{"path .", []sv{{"a", "."}}, root, nil, "a"},
		{"path a/..", []sv{{"a", "a/.."}}, root, nil, "a"},
		{"duplicate name", []sv{{"a", "x"}, {"a", "y"}}, `duplicate service name "a"`, nil, "a"},
		{"duplicate path exact", []sv{{"a", "x"}, {"b", "x"}}, `service "b": path "x" duplicates the path of service "a"`, nil, "a"},
		{"duplicate path unclean", []sv{{"a", "x"}, {"b", "./x/"}}, `duplicates the path of service "a"`, nil, "a"},
		{"duplicate path via dotdot", []sv{{"a", "x"}, {"b", "y/../x"}}, `duplicates the path of service "a"`, nil, "a"},
		{"name ..", []sv{{"..", "x"}}, `service "..": name must be`, nil, ".."},
		{"name .", []sv{{".", "x"}}, badName, nil, "."},
		{"name with slash", []sv{{"a/b", "x"}}, badName, nil, "a/b"},
		{"name with ../", []sv{{"../x", "x"}}, badName, nil, "../x"},
		{"name with backslash", []sv{{`a\b`, "x"}}, badName, nil, `a\b`},
		{"name with NUL", []sv{{"a\x00b", "x"}}, badName, nil, "a\x00b"},
		{"leading dash", []sv{{"-rf", "x"}}, badName, nil, "-rf"},
		{"leading dot", []sv{{".hidden", "x"}}, badName, nil, ".hidden"},
		{"65-char name", []sv{{strings.Repeat("a", 65), "x"}}, badName, nil, strings.Repeat("a", 65)},
		{"space in name", []sv{{"my app", "x"}}, badName, nil, "my app"},
		{"non-ASCII name", []sv{{"caf\u00e9", "x"}}, badName, nil, "caf\u00e9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ServicesConfig{}
			for _, s := range tt.services {
				cfg.Services = append(cfg.Services, Service{Name: s[0], Path: s[1], Enabled: true})
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				for i, want := range tt.wantPaths {
					if got := cfg.Services[i].Path; got != want {
						t.Errorf("service %d path = %q, want %q", i, got, want)
					}
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
			if tt.wantSvc != "" && !strings.Contains(err.Error(), strconv.Quote(tt.wantSvc)) {
				t.Errorf("error %q does not cite service %q", err, tt.wantSvc)
			}
		})
	}
}

func TestServicesValidate_FailureLeavesPathsUntouched(t *testing.T) {
	cfg := ServicesConfig{Services: []Service{
		{Name: "a", Path: "./x/"},
		{Name: "b", Path: ".."},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := cfg.Services[0].Path; got != "./x/" {
		t.Errorf("path was normalised despite failure: %q", got)
	}
}

func TestLoadServices_CleansPaths(t *testing.T) {
	cfg, err := LoadServices("testdata/services_unclean_path.toml")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Services[0].Path; got != "services/demoapp" {
		t.Errorf("path = %q, want services/demoapp", got)
	}
}
