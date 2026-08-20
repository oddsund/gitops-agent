// Package selfupdate checks GitHub for a newer gitops-agent release than
// the one installed, and if there is one, downloads, verifies, and
// installs it, then restarts the service. It's the Go port of
// systemd/update.bash and scripts/lib/github-release.bash -- same steps,
// same verification, same leave-the-installed-binary-alone-on-any-failure
// behaviour, so a host can be moved from the bash path to this one with no
// change in what it checks.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Config controls where Update looks for a release and what it does with
// it. APIBaseURL and DownloadBaseURL default to the real GitHub endpoints
// when empty; tests point them at a fake release server instead.
type Config struct {
	Repo            string // "owner/repo"
	Token           string // empty for an unauthenticated public repo
	AssetName       string // e.g. "gitops-agent-linux-arm64"; empty derives it from runtime.GOARCH
	BinPath         string // e.g. /usr/local/bin/gitops-agent
	VersionFilePath string // e.g. /etc/gitops-agent/installed-version
	ServiceName     string // e.g. gitops-agent
	SkipAttestation bool

	APIBaseURL      string
	DownloadBaseURL string
	HTTPClient      *http.Client
}

// Update is the entry point: check cfg.Repo for a release newer than
// cfg.VersionFilePath records, and if there is one, download, verify, and
// install it, then restart cfg.ServiceName.
func Update(cfg Config) error {
	if cfg.AssetName == "" {
		name, err := defaultAssetName()
		if err != nil {
			return err
		}
		cfg.AssetName = name
	}

	log.Printf("checking for a newer gitops-agent release...")
	latestTag, err := latestReleaseTag(cfg)
	if err != nil {
		return err
	}

	installedTag := ""
	if data, err := os.ReadFile(cfg.VersionFilePath); err == nil {
		installedTag = strings.TrimSpace(string(data))
	}

	if latestTag == installedTag {
		log.Printf("already on %s, nothing to do", latestTag)
		return nil
	}
	log.Printf("installed: %s. latest: %s. updating.", orNone(installedTag), latestTag)

	tempFile, err := fetchVerified(cfg, latestTag, filepath.Dir(cfg.BinPath))
	if err != nil {
		return err
	}
	defer os.Remove(tempFile) // no-op once install() below renames it away

	if err := install(cfg, tempFile, latestTag); err != nil {
		return err
	}

	log.Printf("restarting %s", cfg.ServiceName)
	if err := restartService(cfg.ServiceName); err != nil {
		return err
	}

	log.Printf("updated to %s", latestTag)
	return nil
}

// install makes tempFile executable, renames it into cfg.BinPath (same
// directory as tempFile, so the rename is atomic even while the old binary
// is running -- it won't see the new file until the restart below), and
// records tag as the installed version.
func install(cfg Config, tempFile, tag string) error {
	if err := os.Chmod(tempFile, 0o755); err != nil {
		return fmt.Errorf("making %s executable: %w", tempFile, err)
	}
	if err := os.Rename(tempFile, cfg.BinPath); err != nil {
		return fmt.Errorf("installing %s: %w", cfg.BinPath, err)
	}
	if err := os.WriteFile(cfg.VersionFilePath, []byte(tag+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", cfg.VersionFilePath, err)
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// defaultAssetName maps the running architecture to the release asset
// naming convention (gitops-agent-linux-<arch>), same set update.bash's
// `uname -m` case covers.
func defaultAssetName() (string, error) {
	switch runtime.GOARCH {
	case "arm64", "amd64":
		return "gitops-agent-linux-" + runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("no release asset for this architecture (%s)", runtime.GOARCH)
	}
}

func client(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
	}
	return http.DefaultClient
}

func apiBaseURL(cfg Config) string {
	if cfg.APIBaseURL != "" {
		return cfg.APIBaseURL
	}
	return "https://api.github.com"
}

func downloadBaseURL(cfg Config) string {
	if cfg.DownloadBaseURL != "" {
		return cfg.DownloadBaseURL
	}
	return "https://github.com"
}

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

// latestReleaseTag asks the GitHub API for cfg.Repo's latest release tag.
func latestReleaseTag(cfg Config) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBaseURL(cfg), cfg.Repo)
	var info releaseInfo
	if err := getJSON(cfg, url, &info); err != nil {
		return "", fmt.Errorf("querying latest release: %w", err)
	}
	if info.TagName == "" {
		return "", fmt.Errorf("could not determine the latest release tag from %s", url)
	}
	return info.TagName, nil
}

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type releaseByTag struct {
	Assets []releaseAsset `json:"assets"`
}

// assetID looks up the numeric asset id for assetName in cfg.Repo's
// release tag, via the authenticated API -- only used on the token path,
// since it (unlike the plain releases/download URL) works against a
// private repo.
func assetID(cfg Config, tag, assetName string) (int64, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBaseURL(cfg), cfg.Repo, tag)
	var rel releaseByTag
	if err := getJSON(cfg, url, &rel); err != nil {
		return 0, fmt.Errorf("looking up release %s: %w", tag, err)
	}
	for _, a := range rel.Assets {
		if a.Name == assetName {
			return a.ID, nil
		}
	}
	return 0, fmt.Errorf("release %s has no %s asset", tag, assetName)
}

func getJSON(cfg Config, url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := client(cfg).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchVerified downloads cfg.AssetName from release tag into a temp file
// under destDir, verifies it against the "<asset>.sha256" file published
// alongside it, then (unless cfg.SkipAttestation) its build provenance,
// and returns the verified temp file's path. Mirrors
// github_release_fetch_verified: any failure removes the temp file and
// returns an error, so a caller can never end up installing something
// that failed a check just because it forgot to look at the return value.
func fetchVerified(cfg Config, tag, destDir string) (string, error) {
	dest, err := os.CreateTemp(destDir, "gitops-agent-update-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file in %s: %w", destDir, err)
	}
	destPath := dest.Name()
	dest.Close()
	cleanup := func() { os.Remove(destPath) }

	var assetURL, checksumURL string
	headers := map[string]string{}
	if cfg.Token != "" {
		id, err := assetID(cfg, tag, cfg.AssetName)
		if err != nil {
			cleanup()
			return "", err
		}
		checksumID, err := assetID(cfg, tag, cfg.AssetName+".sha256")
		if err != nil {
			cleanup()
			return "", fmt.Errorf("release %s has no %s.sha256 asset -- refusing to install an unverified binary", tag, cfg.AssetName)
		}
		assetURL = fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBaseURL(cfg), cfg.Repo, id)
		checksumURL = fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBaseURL(cfg), cfg.Repo, checksumID)
		headers["Authorization"] = "Bearer " + cfg.Token
		headers["Accept"] = "application/octet-stream"
	} else {
		assetURL = fmt.Sprintf("%s/%s/releases/download/%s/%s", downloadBaseURL(cfg), cfg.Repo, tag, cfg.AssetName)
		checksumURL = assetURL + ".sha256"
	}

	log.Printf("downloading %s from %s (%s)...", cfg.AssetName, cfg.Repo, tag)
	if err := downloadToFile(cfg, assetURL, headers, destPath); err != nil {
		cleanup()
		return "", fmt.Errorf("release %s has no %s asset: %w", tag, cfg.AssetName, err)
	}
	checksumData, err := downloadToMemory(cfg, checksumURL, headers)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("release %s has no %s.sha256 asset -- refusing to install an unverified binary: %w", tag, cfg.AssetName, err)
	}

	expected := parseChecksum(checksumData)
	if expected == "" {
		cleanup()
		return "", fmt.Errorf("could not parse a checksum out of %s.sha256", cfg.AssetName)
	}
	actual, err := sha256File(destPath)
	if err != nil {
		cleanup()
		return "", err
	}
	if expected != actual {
		cleanup()
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s -- leaving the currently installed binary untouched", cfg.AssetName, expected, actual)
	}
	log.Printf("checksum verified for %s (%s)", cfg.AssetName, actual)

	if cfg.SkipAttestation {
		log.Printf("skipping build provenance verification (GITOPS_AGENT_SKIP_ATTESTATION=1)")
	} else if err := verifyAttestation(cfg.Repo, cfg.Token, destPath); err != nil {
		cleanup()
		return "", err
	}

	return destPath, nil
}

// parseChecksum pulls out just the hash column of a checksum file. The
// published file names the asset as the build produced it, which never
// matches destPath, so sha256sum -c style verification doesn't apply --
// same reasoning as the bash helper's own comment.
func parseChecksum(data []byte) string {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadToFile(cfg Config, url string, headers map[string]string, dest string) error {
	resp, err := doGet(cfg, url, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadToMemory(cfg Config, url string, headers map[string]string) ([]byte, error) {
	resp, err := doGet(cfg, url, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func doGet(cfg Config, url string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client(cfg).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return resp, nil
}

func restartService(name string) error {
	out, err := exec.Command("systemctl", "restart", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restarting %s: %w\n%s", name, err, out)
	}
	return nil
}
