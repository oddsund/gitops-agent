// Package selfupdate implements the selfupdate mechanism currently in systemd/update.bash
// and scripts/github-release.bash
//
// Work-in-progress, currently only has functionality for fetching the latest release tag from
// github.
// Will eventually fetch the new binary from github, and update itself in place.

package selfupdate

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
)

type Config struct {
	// Empty defaults to "oddsund/gitops-agent"
	Repo string
	// Empty defaults to no token
	Token string
	// Empty defaults to production github
	APIBaseURL string
	// Empty defaults to http.DefaultClient
	HTTPClient *http.Client
}

type gitHubResponse struct {
	TagName string `json:"tag_name"`
}

func latestReleaseTag(cfg Config) (string, error) {
	cfg = cfg.populateWithDefaultValues()
	url := fmt.Sprintf("%s/repos/%s/releases/latest", cfg.APIBaseURL, cfg.Repo)
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("could not create request: %w", err)
	}

	request.Header.Add("Accept", "application/vnd.github+json")
	if len(cfg.Token) > 0 {
		request.Header.Add("Authorization", fmt.Sprintf("Bearer %s", cfg.Token))
	}

	resp, err := cfg.HTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("error while calling %s/%s: %w", cfg.APIBaseURL, cfg.Repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("got status %d when calling %s/%s, expected 200", resp.StatusCode, cfg.APIBaseURL, cfg.Repo)
	}

	var gh gitHubResponse
	err = json.NewDecoder(resp.Body).Decode(&gh)

	if err != nil {
		return "", fmt.Errorf("error while deserializing response from %s/%s: %w", cfg.APIBaseURL, cfg.Repo, err)
	}

	if gh.TagName == "" {
		return "", fmt.Errorf("no tag_name returned from %s/%s", cfg.APIBaseURL, cfg.Repo)
	}

	return gh.TagName, nil
}

func (cfg Config) populateWithDefaultValues() Config {
	return Config{
		APIBaseURL: cmp.Or(cfg.APIBaseURL, "https://api.github.com"),
		Repo:       cmp.Or(cfg.Repo, "oddsund/gitops-agent"),
		Token:      cfg.Token,
		HTTPClient: cmp.Or(cfg.HTTPClient, http.DefaultClient),
	}
}
