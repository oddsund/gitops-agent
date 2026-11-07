package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const tempPrefix = "gitops-agent-update-"

func fetchVerified(ctx context.Context, cfg Config, tag, destDir string) (string, error) {
	cfg = cfg.populateWithDefaultValues()

	if err := validateRepo(cfg.Repo); err != nil {
		return "", err
	}
	if err := validateTag(tag); err != nil {
		return "", err
	}
	asset, err := assetNameFor(cfg.AssetName, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	// CreateTemp makes the file 0600, so it cannot be executed until it has
	// been verified and chmodded below.
	assetFile, err := os.CreateTemp(destDir, tempPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("error while creating asset file: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			assetFile.Close()
			os.Remove(assetFile.Name())
		}
	}()

	base := fmt.Sprintf("%s/%s/releases/download/%s/%s", cfg.DownloadBaseURL, cfg.Repo, tag, asset)
	if err := fetchFile(ctx, cfg.HTTPClient, base, assetFile, maxAssetBytes); err != nil {
		return "", fmt.Errorf("error while fetching asset file: %w", err)
	}

	buf := new(bytes.Buffer)
	if err := fetchFile(ctx, cfg.HTTPClient, base+".sha256", buf, maxMetaBytes); err != nil {
		return "", fmt.Errorf("error while fetching %s.sha256 file for %s, refusing to install unverified binary: %w", asset, asset, err)
	}

	shaFile := strings.Fields(buf.String())
	if len(shaFile) != 2 {
		return "", fmt.Errorf("unparsable shafile for asset - wrong content format")
	}
	wantHex := strings.ToLower(shaFile[0])
	want, err := hex.DecodeString(wantHex)
	if err != nil || len(wantHex) != 64 {
		return "", fmt.Errorf("unparsable shafile for asset - digest looks wrong")
	}

	if _, err := assetFile.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("error while seeking to start of %s: %w", assetFile.Name(), err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, assetFile); err != nil {
		return "", fmt.Errorf("error while copying assetFile(%s) content for hashing: %w", assetFile.Name(), err)
	}
	got := h.Sum(nil)
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return "", fmt.Errorf("sha did not match for %s, refusing to install unverified binary. expected %s, got %s", asset, wantHex, hex.EncodeToString(got))
	}

	// Chmod on the descriptor is not subject to the umask.
	if err := assetFile.Chmod(0o755); err != nil {
		return "", fmt.Errorf("error while finalising %s: %w", assetFile.Name(), err)
	}
	if err := assetFile.Sync(); err != nil {
		return "", fmt.Errorf("error while finalising %s: %w", assetFile.Name(), err)
	}
	if err := assetFile.Close(); err != nil {
		return "", fmt.Errorf("error while finalising %s: %w", assetFile.Name(), err)
	}

	cleanup = false
	return assetFile.Name(), nil
}
