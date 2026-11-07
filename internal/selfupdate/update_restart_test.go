package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type ctxCall struct {
	name        string
	canceled    bool
	hasDeadline bool
	remaining   time.Duration
}

// ctxRunner records the state of the context each command receives.
type ctxRunner struct {
	calls []ctxCall
	onRun func(name string)
	fail  map[string]error
}

func (r *ctxRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	c := ctxCall{name: name, canceled: ctx.Err() != nil}
	if d, ok := ctx.Deadline(); ok {
		c.hasDeadline, c.remaining = true, time.Until(d)
	}
	r.calls = append(r.calls, c)
	if r.onRun != nil {
		r.onRun(name)
	}
	return nil, r.fail[name]
}

func TestUpdate_RestartRunsAfterCancelDuringSwap(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg.Runner = &ctxRunner{onRun: func(name string) {
		if name == "/usr/bin/gh" {
			cancel()
		}
	}}

	if err := Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, cfg, "v1.2.3")

	calls := cfg.Runner.(*ctxRunner).calls
	if len(calls) != 2 || calls[1].name != "systemctl" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].canceled {
		t.Error("restart ran with a canceled context")
	}
}

func TestUpdate_CommandsHaveDeadlines(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	runner := &ctxRunner{}
	cfg.Runner = runner

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	limits := map[string]time.Duration{"/usr/bin/gh": attestationTimeout, "systemctl": restartTimeout}
	for _, c := range runner.calls {
		max := limits[c.name]
		if !c.hasDeadline || c.remaining <= 0 || c.remaining > max {
			t.Errorf("%s: deadline = %v, remaining = %v, want within %v", c.name, c.hasDeadline, c.remaining, max)
		}
	}
}

func TestUpdate_RestartFailureSaysManualRestart(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	cfg.Runner = &ctxRunner{fail: map[string]error{"systemctl": errors.New("exit status 5")}}

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), `systemctl restart gitops-agent`) || !strings.Contains(err.Error(), "manually") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdate_UnparsableLatestTagFails(t *testing.T) {
	srv := fakeRelease(t, "v99999999999999999999.0.0", []byte("NEW"))
	cfg, _, logs := newCfg(t, srv)
	writeVersion(t, cfg, "v1.2.3\n")

	err := Update(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "invalid release tag") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(joined(logs), "not a release version") {
		t.Errorf("logs blame the installed version: %v", *logs)
	}
	if srv.downloadHits() != 0 {
		t.Errorf("download hits = %d", srv.downloadHits())
	}
	assertUntouched(t, cfg, []byte("v1.2.3\n"))
}

func TestUpdate_RemovesStaleTempFiles(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	writeVersion(t, cfg, "v1.2.3\n")
	dir := filepath.Dir(cfg.BinPath)

	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gitops-agent-update-123")
	write("gitops-agent-update-456")
	write("gitops-agent-update-fresh")
	write("other-file")
	write("xgitops-agent-update-1")
	old := time.Now().Add(-2 * staleTempAge)
	for _, name := range []string{"gitops-agent-update-123", "gitops-agent-update-456", "other-file", "xgitops-agent-update-1"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "gitops-agent-update-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "gitops-agent-update-link")); err != nil {
		t.Fatal(err)
	}

	if err := Update(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(dirNames(t, dir), ",")
	want := "gitops-agent,gitops-agent-update-dir,gitops-agent-update-fresh,gitops-agent-update-link,other-file,xgitops-agent-update-1"
	if got != want {
		t.Errorf("entries = %s, want %s", got, want)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Errorf("symlink target was touched: %q, %v", b, err)
	}
}

func TestUpdate_NotRootDoesNotSweep(t *testing.T) {
	srv := fakeRelease(t, "v1.2.3", []byte("NEW"))
	cfg, _, _ := newCfg(t, srv)
	cfg.EUID = func() int { return 1 }
	stale := filepath.Join(filepath.Dir(cfg.BinPath), "gitops-agent-update-1")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	_ = Update(t.Context(), cfg)
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("non-root run removed a file: %v", err)
	}
}
