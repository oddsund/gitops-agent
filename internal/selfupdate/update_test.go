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
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/oddsund/gitops-agent/internal/cmdrun"
)

type releaseServer struct {
	*httptest.Server

	mu      sync.Mutex
	hits    map[string]int
	auth    map[string][]string
	corrupt bool
}

func fakeRelease(t *testing.T, tag string, asset []byte) *releaseServer {
	t.Helper()
	rs := &releaseServer{hits: map[string]int{}, auth: map[string][]string{}}
	sum := sha256.Sum256(asset)
	base := "/test/repo/releases/download/" + tag + "/test-asset"

	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits[r.URL.Path]++
		rs.auth[r.URL.Path] = append(rs.auth[r.URL.Path], r.Header.Get("Authorization"))
		corrupt := rs.corrupt
		rs.mu.Unlock()

		switch r.URL.Path {
		case "/repos/test/repo/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
		case base:
			w.Write(asset)
		case base + ".sha256":
			digest := hex.EncodeToString(sum[:])
			if corrupt {
				digest = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s test-asset\n", digest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) setCorrupt() {
	rs.mu.Lock()
	rs.corrupt = true
	rs.mu.Unlock()
}

func (rs *releaseServer) totalHits() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for _, c := range rs.hits {
		n += c
	}
	return n
}

func (rs *releaseServer) downloadHits() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for p, c := range rs.hits {
		if strings.Contains(p, "/releases/download/") {
			n += c
		}
	}
	return n
}

func newCfg(t *testing.T, srv *releaseServer) (Config, *cmdrun.Fake, *[]string) {
	t.Helper()
	binDir := t.TempDir()
	cfgDir := t.TempDir()
	binPath := filepath.Join(binDir, "gitops-agent")
	if err := os.WriteFile(binPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &cmdrun.Fake{}
	var logs []string
	return Config{
		APIBaseURL:      srv.URL,
		DownloadBaseURL: srv.URL,
		AssetName:       "test-asset",
		Repo:            "test/repo",
		HTTPClient:      srv.Client(),
		BinPath:         binPath,
		VersionFilePath: filepath.Join(cfgDir, "installed-version"),
		Runner:          fake,
		Logf:            func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		LookPath:        func(string) (string, error) { return "/usr/bin/gh", nil },
		EUID:            func() int { return 0 },
	}, fake, &logs
}

func writeVersion(t *testing.T, cfg Config, v string) {
	t.Helper()
	if err := os.WriteFile(cfg.VersionFilePath, []byte(v), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertUntouched checks that neither the binary nor the version file
// changed and that no temp files are left behind. wantVersionFile nil means
// the version file must not exist.
func assertUntouched(t *testing.T, cfg Config, wantVersionFile []byte) {
	t.Helper()
	got, err := os.ReadFile(cfg.BinPath)
	if err != nil || string(got) != "OLD" {
		t.Errorf("binary = %q, %v; want OLD", got, err)
	}
	if names := dirNames(t, filepath.Dir(cfg.BinPath)); !reflect.DeepEqual(names, []string{"gitops-agent"}) {
		t.Errorf("bin dir entries = %v", names)
	}

	vf, err := os.ReadFile(cfg.VersionFilePath)
	if wantVersionFile == nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("version file should not exist, got %q, %v", vf, err)
		}
		if names := dirNames(t, filepath.Dir(cfg.VersionFilePath)); len(names) != 0 {
			t.Errorf("version dir entries = %v", names)
		}
		return
	}
	if err != nil || string(vf) != string(wantVersionFile) {
		t.Errorf("version file = %q, %v; want %q", vf, err, wantVersionFile)
	}
	if names := dirNames(t, filepath.Dir(cfg.VersionFilePath)); len(names) != 1 {
		t.Errorf("version dir entries = %v", names)
	}
}

func joined(logs *[]string) string { return strings.Join(*logs, "\n") }

func assertInstalled(t *testing.T, cfg Config, tag string) {
	t.Helper()
	got, err := os.ReadFile(cfg.BinPath)
	if err != nil || string(got) != "NEW" {
		t.Errorf("binary = %q, %v; want NEW", got, err)
	}
	fi, err := os.Stat(cfg.BinPath)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("binary mode = %v, %v; want 0755", fi.Mode(), err)
	}
	vf, err := os.ReadFile(cfg.VersionFilePath)
	if err != nil || string(vf) != tag+"\n" {
		t.Errorf("version file = %q, %v; want %q", vf, err, tag+"\n")
	}
	fi, err = os.Stat(cfg.VersionFilePath)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("version file mode = %v, %v; want 0644", fi.Mode(), err)
	}
	if names := dirNames(t, filepath.Dir(cfg.BinPath)); !reflect.DeepEqual(names, []string{"gitops-agent"}) {
		t.Errorf("bin dir entries = %v", names)
	}
	if names := dirNames(t, filepath.Dir(cfg.VersionFilePath)); !reflect.DeepEqual(names, []string{"installed-version"}) {
		t.Errorf("version dir entries = %v", names)
	}
}

func TestUpdate_FreshInstall(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, fake, _ := newCfg(t, srv)

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, cfg, "v1.2.3")

	if len(fake.Calls) != 2 {
		t.Fatalf("calls = %+v", fake.Calls)
	}
	gh := fake.Calls[0]
	if gh.Name != "/usr/bin/gh" || len(gh.Args) != 10 {
		t.Fatalf("gh call = %+v", gh)
	}
	wantArgs := []string{
		"attestation", "verify", gh.Args[2],
		"-R", "test/repo",
		"--signer-workflow", "test/repo/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v1.2.3",
		"--deny-self-hosted-runners",
	}
	if !reflect.DeepEqual(gh.Args, wantArgs) {
		t.Errorf("gh args = %v, want %v", gh.Args, wantArgs)
	}
	if !filepath.IsAbs(gh.Args[2]) || filepath.Dir(gh.Args[2]) != filepath.Dir(cfg.BinPath) {
		t.Errorf("attested path %q should be absolute and in the bin dir", gh.Args[2])
	}
	want := cmdrun.Call{Name: "systemctl", Args: []string{"restart", "gitops-agent"}}
	if !reflect.DeepEqual(fake.Calls[1], want) {
		t.Errorf("restart call = %+v, want %+v", fake.Calls[1], want)
	}
}

func TestUpdate_Upgrade(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, logs := newCfg(t, srv)
	writeVersion(t, cfg, "v1.2.2\n")

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, cfg, "v1.2.3")
	if !strings.Contains(joined(logs), "installed: v1.2.2, latest: v1.2.3, updating") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestUpdate_SameVersionNoop(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, fake, logs := newCfg(t, srv)
	writeVersion(t, cfg, "v1.2.3\n")

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if srv.downloadHits() != 0 || len(fake.Calls) != 0 {
		t.Errorf("downloads = %d, calls = %v", srv.downloadHits(), fake.Calls)
	}
	if !strings.Contains(joined(logs), "already on v1.2.3, nothing to do") {
		t.Errorf("logs = %v", *logs)
	}
	assertUntouched(t, cfg, []byte("v1.2.3\n"))
}

func TestUpdate_DowngradeRefused(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, fake, logs := newCfg(t, srv)
	writeVersion(t, cfg, "v1.3.0\n")

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WARNING", "not downgrading", "--force"} {
		if !strings.Contains(joined(logs), want) {
			t.Errorf("logs missing %q: %v", want, *logs)
		}
	}
	if srv.downloadHits() != 0 || len(fake.Calls) != 0 {
		t.Errorf("downloads = %d, calls = %v", srv.downloadHits(), fake.Calls)
	}
	assertUntouched(t, cfg, []byte("v1.3.0\n"))
}

func TestUpdate_Force(t *testing.T) {
	for _, installed := range []string{"v1.2.3", "v1.3.0"} {
		t.Run(installed, func(t *testing.T) {
			srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
			cfg, fake, logs := newCfg(t, srv)
			cfg.Force = true
			writeVersion(t, cfg, installed+"\n")

			if err := Update(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			assertInstalled(t, cfg, "v1.2.3")
			if !strings.Contains(joined(logs), "--force") {
				t.Errorf("logs = %v", *logs)
			}
			if last := fake.Calls[len(fake.Calls)-1]; last.Name != "systemctl" {
				t.Errorf("last call = %+v", last)
			}
		})
	}
}

func TestUpdate_UnparsableInstalled(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, logs := newCfg(t, srv)
	writeVersion(t, cfg, "dev\n")

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, cfg, "v1.2.3")
	if !strings.Contains(joined(logs), "not a release version") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestUpdate_ChecksumMismatch(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	srv.setCorrupt()
	cfg, fake, _ := newCfg(t, srv)

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "sha did not match") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("calls = %v", fake.Calls)
	}
	assertUntouched(t, cfg, nil)
}

func TestUpdate_AttestationFailure(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, fake, _ := newCfg(t, srv)
	fake.Func = func(c cmdrun.Call) ([]byte, error) {
		if c.Name == "/usr/bin/gh" {
			return []byte("no attestation found"), errors.New("exit status 1")
		}
		return nil, nil
	}
	writeVersion(t, cfg, "v1.2.2\n")

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "build provenance verification failed") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range fake.Calls {
		if c.Name == "systemctl" {
			t.Errorf("service restarted after failed attestation")
		}
	}
	assertUntouched(t, cfg, []byte("v1.2.2\n"))
}

func TestUpdate_RestartFailure(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, fake, logs := newCfg(t, srv)
	fake.Func = func(c cmdrun.Call) ([]byte, error) {
		if c.Name == "systemctl" {
			return []byte("unit not found"), errors.New("exit status 5")
		}
		return nil, nil
	}

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "restarting gitops-agent failed") {
		t.Fatalf("err = %v", err)
	}
	assertInstalled(t, cfg, "v1.2.3")
	if !strings.Contains(joined(logs), "ERROR") {
		t.Errorf("logs = %v", *logs)
	}
}

func TestUpdate_VersionFileDirMissing(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	cfg.VersionFilePath = filepath.Join(t.TempDir(), "missing", "installed-version")

	if err := Update(t.Context(), cfg); err == nil {
		t.Fatal("expected error")
	}
	if srv.totalHits() != 0 {
		t.Errorf("hits = %d", srv.totalHits())
	}
	if got, _ := os.ReadFile(cfg.BinPath); string(got) != "OLD" {
		t.Errorf("binary = %q", got)
	}
}

func TestUpdate_NotRoot(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	cfg.EUID = func() int { return 1 }

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("err = %v", err)
	}
	if srv.totalHits() != 0 {
		t.Errorf("hits = %d", srv.totalHits())
	}
}

func TestUpdate_RelativePaths(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))

	cfg, _, _ := newCfg(t, srv)
	cfg.BinPath = "gitops-agent"
	if err := Update(t.Context(), cfg); err == nil {
		t.Error("relative BinPath should fail")
	}

	cfg, _, _ = newCfg(t, srv)
	cfg.VersionFilePath = "installed-version"
	if err := Update(t.Context(), cfg); err == nil {
		t.Error("relative VersionFilePath should fail")
	}
	if srv.totalHits() != 0 {
		t.Errorf("hits = %d", srv.totalHits())
	}
}

func TestUpdate_BadServiceName(t *testing.T) {
	for _, name := range []string{"--now", "a b"} {
		srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
		cfg, _, _ := newCfg(t, srv)
		cfg.ServiceName = name
		if err := Update(t.Context(), cfg); err == nil {
			t.Errorf("service %q should fail", name)
		}
		if srv.totalHits() != 0 {
			t.Errorf("hits = %d", srv.totalHits())
		}
	}
}

func TestUpdate_BadTagFromAPI(t *testing.T) {
	srv := fakeRelease(t, "../../evil", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)

	if err := Update(t.Context(), cfg); err == nil {
		t.Fatal("expected error")
	}
	if srv.downloadHits() != 0 {
		t.Errorf("download hits = %d", srv.downloadHits())
	}
}

func TestUpdate_ContextCanceled(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := Update(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, cfg, nil)
}

func TestUpdate_TokenNotLeaked(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	srv.setCorrupt()
	cfg, fake, logs := newCfg(t, srv)
	cfg.Token = "s3cret"

	err := Update(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "s3cret") || strings.Contains(joined(logs), "s3cret") {
		t.Errorf("token leaked: %v, %v", err, *logs)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if got := srv.auth["/repos/test/repo/releases/latest"]; len(got) != 1 || got[0] != "Bearer s3cret" {
		t.Errorf("API auth = %v", got)
	}
	for p, vals := range srv.auth {
		if !strings.Contains(p, "/releases/download/") {
			continue
		}
		for _, v := range vals {
			if v != "" {
				t.Errorf("download %s got Authorization %q", p, v)
			}
		}
	}
	for _, c := range fake.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, "s3cret") {
				t.Errorf("token in argv: %+v", c)
			}
		}
	}
}
