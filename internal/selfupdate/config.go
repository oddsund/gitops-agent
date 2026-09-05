package selfupdate

import (
	"cmp"
	"net/http"
)

type Config struct {
	// Empty defaults to production github
	APIBaseURL string
	// Defaults to gitops-agent-linux-arm64
	AssetName string
	// Defaults to github.com
	DownloadBaseURL string
	// Empty defaults to "oddsund/gitops-agent"
	Repo string
	// Empty defaults to no token
	Token string
	// Empty defaults to http.DefaultClient
	HTTPClient *http.Client
}

func (cfg Config) populateWithDefaultValues() Config {
	return Config{
		APIBaseURL:      cmp.Or(cfg.APIBaseURL, "https://api.github.com"),
		AssetName:       cmp.Or(cfg.AssetName, "gitops-agent-linux-arm64"),
		DownloadBaseURL: cmp.Or(cfg.DownloadBaseURL, "https://github.com"),
		Repo:            cmp.Or(cfg.Repo, "oddsund/gitops-agent"),
		Token:           cfg.Token,
		HTTPClient:      cmp.Or(cfg.HTTPClient, http.DefaultClient),
	}
}
