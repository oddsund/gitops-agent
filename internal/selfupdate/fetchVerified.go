package selfupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func fetchVerified(cfg Config, tag, destDir string) (string, error) {
	cfg = cfg.populateWithDefaultValues()

	assetFile, err := os.CreateTemp(destDir, "gitops-agent-update-*")
	if err != nil {
		return "", fmt.Errorf("error while creating asset file: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(assetFile.Name())
		}
	}()
	defer assetFile.Close()

	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", cfg.DownloadBaseURL, cfg.Repo, tag, cfg.AssetName)
	err = fetchFile(cfg.HTTPClient, url, cfg.Token, assetFile)

	if err != nil {
		return "", fmt.Errorf("error while fetching asset file: %w", err)
	}

	buf := new(bytes.Buffer)

	url = fmt.Sprintf("%s/%s/releases/download/%s/%s.sha256", cfg.DownloadBaseURL, cfg.Repo, tag, cfg.AssetName)
	err = fetchFile(cfg.HTTPClient, url, cfg.Token, buf)

	if err != nil {
		return "", fmt.Errorf("error while fetching %s.sha256 file for %s, refusing to install unverified binary: %w", cfg.AssetName, cfg.AssetName, err)
	}

	shaFile := strings.Fields(buf.String())

	if len(shaFile) != 2 {
		return "", fmt.Errorf("unparsable shafile for asset - wrong content format")
	}

	if len(shaFile[0]) != 64 {
		return "", fmt.Errorf("unparsable shafile for asset - digest looks wrong")
	}

	if _, err = assetFile.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("error while seeking to start of %s: %w", assetFile.Name(), err)
	}

	h := sha256.New()
	if _, err = io.Copy(h, assetFile); err != nil {
		return "", fmt.Errorf("error while copying assetFile(%s) content for hashing: %w", assetFile.Name(), err)
	}
	assetSha := hex.EncodeToString(h.Sum(nil))
	if assetSha != shaFile[0] {
		return "", fmt.Errorf("sha did not match for %s, refusing to install unverified binary. expected %s, got %s", cfg.AssetName, shaFile[0], assetSha)
	}

	cleanup = false
	return assetFile.Name(), nil
}

func fetchFile(client *http.Client, url, token string, destFile io.Writer) error {
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("could not create request: %w", err)
	}

	request.Header.Add("Accept", "application/octet-stream")
	if len(token) > 0 {
		request.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	resp, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("error while calling %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("got status %d when calling %s, expected 200", resp.StatusCode, url)
	}

	_, err = io.Copy(destFile, resp.Body)

	if err != nil {
		return fmt.Errorf("error while reading file from %s: %w", url, err)
	}

	return nil
}
