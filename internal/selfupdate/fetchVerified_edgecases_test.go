package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type download struct {
	asset  []byte
	sha    string // sha256 file content; empty means the correct digest
	hits   atomic.Int32
	mu     sync.Mutex
	auth   map[string]string
	paths  []string
	Server *httptest.Server
}

func newDownload(t *testing.T, asset []byte, sha string) *download {
	t.Helper()
	d := &download{asset: asset, sha: sha, auth: map[string]string{}}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		d.mu.Lock()
		d.auth[r.URL.Path] = r.Header.Get("Authorization")
		d.paths = append(d.paths, r.URL.Path)
		d.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			if d.sha != "" {
				w.Write([]byte(d.sha))
				return
			}
			fmt.Fprintf(w, "%s test-asset\n", digest(d.asset))
			return
		}
		w.Write(d.asset)
	}))
	t.Cleanup(d.Server.Close)
	return d
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (d *download) cfg() Config {
	return Config{
		AssetName:       "test-asset",
		DownloadBaseURL: d.Server.URL,
		Repo:            "test/repo",
		HTTPClient:      d.Server.Client(),
	}
}

func TestFetchVerified_Mode(t *testing.T) {
	d := newDownload(t, []byte("Testfil"), "")
	res, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(res)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v; want 0755", fi.Mode(), err)
	}
}

func TestFetchVerified_UppercaseDigestAccepted(t *testing.T) {
	asset := []byte("Testfil")
	d := newDownload(t, asset, strings.ToUpper(digest(asset))+" test-asset")
	if _, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestFetchVerified_NonHexDigest(t *testing.T) {
	d := newDownload(t, []byte("Testfil"), strings.Repeat("a", 30)+"zz"+strings.Repeat("a", 32)+" test-asset")
	dir := t.TempDir()
	_, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", dir)
	if err == nil || !strings.Contains(err.Error(), "digest looks wrong") {
		t.Fatalf("err = %v", err)
	}
	verifyNoFilesInDir(t, dir)
}

func TestFetchVerified_AssetTooLarge(t *testing.T) {
	lowerLimits(t, 8, maxMetaBytes)

	d := newDownload(t, []byte("123456789"), "")
	dir := t.TempDir()
	_, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", dir)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v", err)
	}
	verifyNoFilesInDir(t, dir)

	exact := newDownload(t, []byte("12345678"), "")
	if _, err := fetchVerified(t.Context(), exact.cfg(), "v1.2.3", t.TempDir()); err != nil {
		t.Fatalf("an asset exactly at the limit should pass: %v", err)
	}
}

func TestFetchVerified_ChecksumFileTooLarge(t *testing.T) {
	lowerLimits(t, maxAssetBytes, 16)
	d := newDownload(t, []byte("Testfil"), "")
	dir := t.TempDir()
	if _, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", dir); err == nil {
		t.Fatal("expected error")
	}
	verifyNoFilesInDir(t, dir)
}

func TestFetchVerified_InvalidTag(t *testing.T) {
	for _, tag := range []string{"", "../x", "v1.2.3/../x"} {
		d := newDownload(t, []byte("Testfil"), "")
		dir := t.TempDir()
		if _, err := fetchVerified(t.Context(), d.cfg(), tag, dir); err == nil {
			t.Errorf("tag %q should fail", tag)
		}
		if d.hits.Load() != 0 {
			t.Errorf("tag %q caused %d requests", tag, d.hits.Load())
		}
		verifyNoFilesInDir(t, dir)
	}
}

func TestFetchVerified_NoAuthOnDownloads(t *testing.T) {
	d := newDownload(t, []byte("Testfil"), "")
	cfg := d.cfg()
	cfg.Token = "tok"
	if _, err := fetchVerified(t.Context(), cfg, "v1.2.3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.auth) != 2 {
		t.Fatalf("saw %d paths, want 2", len(d.auth))
	}
	for p, a := range d.auth {
		if a != "" {
			t.Errorf("%s got Authorization %q", p, a)
		}
	}
}

func TestFetchVerified_RedirectNoAuth(t *testing.T) {
	target := newDownload(t, []byte("Testfil"), "")
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("front saw Authorization on %s", r.URL.Path)
		}
		http.Redirect(w, r, target.Server.URL+r.URL.Path, http.StatusFound)
	}))
	defer front.Close()

	cfg := target.cfg()
	cfg.DownloadBaseURL = front.URL
	cfg.Token = "tok"
	if _, err := fetchVerified(t.Context(), cfg, "v1.2.3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	for p, a := range target.auth {
		if a != "" {
			t.Errorf("%s got Authorization %q", p, a)
		}
	}
}

func TestFetchVerified_DefaultAssetName(t *testing.T) {
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		t.Skipf("no release asset for %s", runtime.GOARCH)
	}
	d := newDownload(t, []byte("Testfil"), "")
	cfg := d.cfg()
	cfg.AssetName = ""
	if _, err := fetchVerified(t.Context(), cfg, "v1.2.3", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if want := "gitops-agent-linux-" + runtime.GOARCH; !strings.HasSuffix(d.paths[0], "/"+want) {
		t.Errorf("first request path = %q, want suffix %q", d.paths[0], want)
	}
}

func TestFetchVerified_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	dir := t.TempDir()
	_, err := fetchVerified(ctx, Config{
		AssetName: "test-asset", DownloadBaseURL: server.URL, Repo: "test/repo", HTTPClient: server.Client(),
	}, "v1.2.3", dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	verifyNoFilesInDir(t, dir)
}

func TestFetchVerified_MissingDestDir(t *testing.T) {
	d := newDownload(t, []byte("Testfil"), "")
	_, err := fetchVerified(t.Context(), d.cfg(), "v1.2.3", t.TempDir()+"/missing")
	if err == nil || !strings.Contains(err.Error(), "error while creating asset file") {
		t.Fatalf("err = %v", err)
	}
	if d.hits.Load() != 0 {
		t.Errorf("hits = %d", d.hits.Load())
	}
}

func TestFetchVerified_ErrorContainsRealURL(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := fetchVerified(t.Context(), Config{
		AssetName: "test-asset", DownloadBaseURL: server.URL, Repo: "test/repo", HTTPClient: server.Client(),
	}, "v1.2.3", t.TempDir())
	want := server.URL + "/test/repo/releases/download/v1.2.3/test-asset"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %s", err, want)
	}
}
