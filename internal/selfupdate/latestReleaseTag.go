package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
)

type gitHubResponse struct {
	TagName string `json:"tag_name"`
}

func latestReleaseTag(ctx context.Context, cfg Config) (string, error) {
	cfg = cfg.populateWithDefaultValues()
	if err := validateRepo(cfg.Repo); err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/repos/%s/releases/latest", cfg.APIBaseURL, cfg.Repo)
	resp, err := doGet(ctx, cfg.HTTPClient, url, "application/vnd.github+json", cfg.Token)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, maxMetaBytes)
	if err != nil {
		return "", fmt.Errorf("reading response from %s: %w", url, err)
	}

	var gh gitHubResponse
	if err := json.Unmarshal(body, &gh); err != nil {
		return "", fmt.Errorf("error while deserializing response from %s: %w", url, err)
	}

	if gh.TagName == "" {
		return "", fmt.Errorf("no tag_name returned from %s", url)
	}

	if err := validateTag(gh.TagName); err != nil {
		return "", err
	}
	return gh.TagName, nil
}
