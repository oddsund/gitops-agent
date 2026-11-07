package selfupdate

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPopulateDefaults(t *testing.T) {
	cfg := Config{}.populateWithDefaultValues()

	checks := map[string][2]string{
		"APIBaseURL":      {cfg.APIBaseURL, "https://api.github.com"},
		"DownloadBaseURL": {cfg.DownloadBaseURL, "https://github.com"},
		"Repo":            {cfg.Repo, "oddsund/gitops-agent"},
		"BinPath":         {cfg.BinPath, "/usr/local/bin/gitops-agent"},
		"VersionFilePath": {cfg.VersionFilePath, "/etc/gitops-agent/installed-version"},
		"ServiceName":     {cfg.ServiceName, "gitops-agent"},
		"AssetName":       {cfg.AssetName, ""},
		"Token":           {cfg.Token, ""},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if cfg.HTTPClient == nil || cfg.HTTPClient.Timeout != 5*time.Minute {
		t.Errorf("HTTPClient = %+v, want a 5 minute timeout", cfg.HTTPClient)
	}
	if cfg.Runner == nil || cfg.Logf == nil || cfg.LookPath == nil || cfg.EUID == nil {
		t.Errorf("function fields should all be defaulted: %+v", cfg)
	}

	own := &http.Client{Timeout: time.Second}
	if got := (Config{HTTPClient: own}).populateWithDefaultValues().HTTPClient; got != own {
		t.Errorf("caller-supplied client was replaced")
	}
}

func TestAssetNameFor(t *testing.T) {
	cases := []struct {
		override, goarch, want, wantErr string
	}{
		{"", "arm64", "gitops-agent-linux-arm64", ""},
		{"", "amd64", "gitops-agent-linux-amd64", ""},
		{"", "riscv64", "", "riscv64"},
		{"custom", "riscv64", "custom", ""},
		{"../x", "arm64", "", "invalid asset name"},
	}
	for _, tc := range cases {
		got, err := assetNameFor(tc.override, tc.goarch)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("assetNameFor(%q, %q) error = %v, want %q", tc.override, tc.goarch, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("assetNameFor(%q, %q) = %q, %v; want %q", tc.override, tc.goarch, got, err, tc.want)
		}
	}
}
