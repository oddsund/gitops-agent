package selfupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeRelease serves a GitHub-shaped API: /repos/<repo>/releases/latest and
// the plain releases/download URLs github_release_fetch_verified's
// unauthenticated path uses. Good enough to exercise the real HTTP code in
// Update without touching the network.
func fakeRelease(t *testing.T, repo, tag, assetName string, assetContent []byte, checksum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	downloadPrefix := "/" + repo + "/releases/download/" + tag + "/"
	mux.HandleFunc(downloadPrefix+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(assetContent)
	})
	mux.HandleFunc(downloadPrefix+assetName+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", checksum, assetName)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// stubPathWith puts fake binaries on PATH (replacing it entirely, so a
// real gh on the host machine can never leak into a test) that each record
// their argv to <name>.args and exit with exitCode.
func stubPathWith(t *testing.T, exitCodes map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	for name, code := range exitCodes {
		script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, name+".args") + "\nexit " + strconv.Itoa(code) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

func baseConfig(srv *httptest.Server, binPath, versionPath string) Config {
	return Config{
		Repo:            "test/gitops-agent",
		AssetName:       "gitops-agent-linux-arm64",
		BinPath:         binPath,
		VersionFilePath: versionPath,
		ServiceName:     "gitops-agent",
		APIBaseURL:      srv.URL,
		DownloadBaseURL: srv.URL,
	}
}

func TestUpdate_AlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")
	if err := os.WriteFile(versionPath, []byte("v1.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No download/checksum routes registered: a request for either would
	// 404 and fail the test, proving Update didn't even try.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/test/gitops-agent/releases/latest" {
			fmt.Fprint(w, `{"tag_name": "v1.2.0"}`)
			return
		}
		t.Errorf("unexpected request for %s when already up to date", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	stubPathWith(t, map[string]int{"systemctl": 0})

	cfg := baseConfig(srv, binPath, versionPath)
	if err := Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := os.ReadFile(binPath)
	if err != nil || string(got) != "old binary" {
		t.Errorf("bin path = %q, %v; want untouched", got, err)
	}
}

func TestUpdate_InstallsNewerRelease(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")
	if err := os.WriteFile(versionPath, []byte("v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assetContent := []byte("fake gitops-agent binary v1.2.0")
	srv := fakeRelease(t, "test/gitops-agent", "v1.2.0", "gitops-agent-linux-arm64", assetContent, sha256Hex(assetContent))
	stubDir := stubPathWith(t, map[string]int{"systemctl": 0})

	cfg := baseConfig(srv, binPath, versionPath)
	if err := Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if !bytes.Equal(got, assetContent) {
		t.Errorf("installed binary = %q, want %q", got, assetContent)
	}
	if fi, err := os.Stat(binPath); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", err)
	}

	gotVersion, err := os.ReadFile(versionPath)
	if err != nil || strings.TrimSpace(string(gotVersion)) != "v1.2.0" {
		t.Errorf("version file = %q, %v; want v1.2.0", gotVersion, err)
	}

	restartArgs, err := os.ReadFile(filepath.Join(stubDir, "systemctl.args"))
	if err != nil || strings.TrimSpace(string(restartArgs)) != "restart gitops-agent" {
		t.Errorf("systemctl args = %q, %v; want %q", restartArgs, err, "restart gitops-agent")
	}
}

func TestUpdate_ChecksumMismatchAborts(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")
	if err := os.WriteFile(binPath, []byte("original binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	assetContent := []byte("fake gitops-agent binary v1.2.0")
	srv := fakeRelease(t, "test/gitops-agent", "v1.2.0", "gitops-agent-linux-arm64", assetContent, "0000000000000000000000000000000000000000000000000000000000000000")
	stubPathWith(t, map[string]int{"systemctl": 0})

	cfg := baseConfig(srv, binPath, versionPath)
	err := Update(cfg)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Update err = %v, want a checksum mismatch error", err)
	}

	got, err := os.ReadFile(binPath)
	if err != nil || string(got) != "original binary" {
		t.Errorf("bin path = %q, %v; want untouched after a checksum mismatch", got, err)
	}
	if _, err := os.ReadFile(versionPath); !os.IsNotExist(err) {
		t.Errorf("version file should not have been written after a checksum mismatch")
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gitops-agent-update-") {
			t.Errorf("leftover temp file %s after a checksum mismatch", e.Name())
		}
	}
}

func TestUpdate_MissingGHSkipsAttestation(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")

	assetContent := []byte("fake gitops-agent binary v1.2.0")
	srv := fakeRelease(t, "test/gitops-agent", "v1.2.0", "gitops-agent-linux-arm64", assetContent, sha256Hex(assetContent))
	// Only systemctl on PATH -- no gh binary anywhere.
	stubPathWith(t, map[string]int{"systemctl": 0})

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg := baseConfig(srv, binPath, versionPath)
	if err := Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if !strings.Contains(logBuf.String(), "gh not found") {
		t.Errorf("log output = %q, want it to mention gh being skipped", logBuf.String())
	}
	if _, err := os.ReadFile(binPath); err != nil {
		t.Errorf("binary should still be installed when gh is missing: %v", err)
	}
}

func TestUpdate_AttestationFailureAborts(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")
	if err := os.WriteFile(binPath, []byte("original binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	assetContent := []byte("fake gitops-agent binary v1.2.0")
	srv := fakeRelease(t, "test/gitops-agent", "v1.2.0", "gitops-agent-linux-arm64", assetContent, sha256Hex(assetContent))
	stubPathWith(t, map[string]int{"systemctl": 0, "gh": 1})

	cfg := baseConfig(srv, binPath, versionPath)
	err := Update(cfg)
	if err == nil || !strings.Contains(err.Error(), "build provenance verification failed") {
		t.Fatalf("Update err = %v, want a build provenance error", err)
	}

	got, err := os.ReadFile(binPath)
	if err != nil || string(got) != "original binary" {
		t.Errorf("bin path = %q, %v; want untouched after a failed attestation", got, err)
	}
}

func TestUpdate_SkipAttestationEnvBypassesAFailingGH(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "gitops-agent")
	versionPath := filepath.Join(dir, "installed-version")

	assetContent := []byte("fake gitops-agent binary v1.2.0")
	srv := fakeRelease(t, "test/gitops-agent", "v1.2.0", "gitops-agent-linux-arm64", assetContent, sha256Hex(assetContent))
	// gh is present but would fail -- SkipAttestation must mean it's never invoked.
	stubPathWith(t, map[string]int{"systemctl": 0, "gh": 1})

	cfg := baseConfig(srv, binPath, versionPath)
	cfg.SkipAttestation = true
	if err := Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := os.ReadFile(binPath); err != nil {
		t.Errorf("binary should be installed when attestation is skipped: %v", err)
	}
}
