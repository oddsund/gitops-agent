package reconcile

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/notify"
)

const canary = "s3cr3t-Canary-9f2a1b"

// realRunner adapts deploy.Runner (real os/exec, fake docker on PATH) to
// the Deployer interface.
type realRunner struct{ r deploy.Runner }

func (d realRunner) Deploy(dir string) error { return d.r.Deploy(dir) }
func (d realRunner) Down(dir string) error   { return d.r.Down(dir) }

// redactHarness wires a real deploy.Runner into the loop. The fake docker
// prints the canary on stderr and exits 1 when <tmp>/fail-<action> exists.
func redactHarness(t *testing.T) (*harness, *fakeNotifier, string) {
	t.Helper()
	tmp := t.TempDir()
	h := newHarness(t, "demo:apps/demo:true")
	h.r.Config.ClonePath = filepath.Join(tmp, "clone")
	if err := os.MkdirAll(filepath.Join(h.r.Config.ClonePath, "apps", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.r.Config.ClonePath, "apps", "demo", "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(tmp, "secrets")
	if err := os.MkdirAll(filepath.Join(secrets, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "demo", "secrets.env"), []byte("API_KEY="+canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do last=$a; done\n" +
		"for a in \"$@\"; do case $a in up|down) act=$a;; esac; done\n" +
		"if [ -e " + tmp + "/fail-$act ]; then echo 'error parsing env_file: unexpected \"API_KEY=" + canary + "\"' >&2; exit 1; fi\n" +
		"echo 'ok' >&2\n"
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+":/usr/bin:/bin")

	h.r.Deployer = realRunner{deploy.Runner{Log: h.r.Log, SecretsDir: secrets}}
	h.r.StatusFile = filepath.Join(tmp, "status.json")
	n := &fakeNotifier{}
	h.r.Notifier = n
	return h, n, tmp
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr.Code, rr.Body.String()
}

// assertSurfacesSafe checks every place the error text can reach.
func assertSurfacesSafe(t *testing.T, h *harness, n *fakeNotifier, stage, summary, detailMsg string) {
	t.Helper()
	snap := h.r.Tracker.Snapshot()
	var svcErr string
	for _, s := range snap.Services {
		if s.Name == "demo" {
			svcErr = s.LastError
		}
	}
	if svcErr != summary {
		t.Errorf("tracker LastError = %q, want %q", svcErr, summary)
	}
	if !strings.Contains(snap.LastCycleError, summary) || strings.Contains(snap.LastCycleError, canary) {
		t.Errorf("LastCycleError = %q", snap.LastCycleError)
	}

	code, body := get(t, h.r.Tracker.Handler(), "/status.json")
	if code != 200 || strings.Contains(body, canary) || !strings.Contains(body, summary) {
		t.Errorf("/status.json (%d): %s", code, body)
	}
	code, body = get(t, h.r.Tracker.Handler(), "/")
	if code != 200 || strings.Contains(body, canary) || !strings.Contains(body, "see the journal") {
		t.Errorf("HTML (%d): %s", code, body)
	}
	code, body = get(t, h.r.Tracker.Handler(), "/healthz")
	if code != http.StatusServiceUnavailable || !strings.HasPrefix(body, "unhealthy: ") || strings.Contains(body, canary) {
		t.Errorf("/healthz (%d): %s", code, body)
	}
	raw, err := os.ReadFile(h.r.StatusFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canary) || !strings.Contains(string(raw), "see the journal") {
		t.Errorf("status file: %s", raw)
	}

	if len(n.obs) == 0 {
		t.Fatal("no observation")
	}
	st := findState(t, n.obs[len(n.obs)-1], notify.Key{Kind: notify.KindService, Name: "demo"})
	if st.Stage != stage || strings.Contains(st.Err, canary) || st.Err != summary {
		t.Errorf("observation state = %+v", st)
	}

	logs := h.logs.String()
	if strings.Contains(logs, canary) {
		t.Errorf("logs leak the canary:\n%s", logs)
	}
	var found bool
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, detailMsg) {
			found = strings.Contains(l, "detail=") && strings.Contains(l, "***")
		}
	}
	if !found {
		t.Errorf("no %q line with a scrubbed detail in logs:\n%s", detailMsg, logs)
	}
}

func TestRun_DeployErrorCanaryAbsentEverywhere(t *testing.T) {
	h, n, tmp := redactHarness(t)
	if err := os.WriteFile(filepath.Join(tmp, "fail-up"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, h.r.Run(cancelled(), nil))
	assertSurfacesSafe(t, h, n, "deploy", "docker compose up failed with exit code 1; see the journal", `msg="deploy failed"`)
}

func TestRun_TeardownErrorCanaryAbsentEverywhere(t *testing.T) {
	h, n, tmp := redactHarness(t)
	mustNoErr(t, h.r.Run(cancelled(), nil))

	// Disable the service; down now fails with the canary on stderr.
	h.loader.cfg = svcs("demo:apps/demo:false")
	h.syncer.steps = []syncStep{{res: result(true)}}
	if err := os.WriteFile(filepath.Join(tmp, "fail-down"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, h.r.Run(cancelled(), nil))
	assertSurfacesSafe(t, h, n, "teardown", "docker compose down failed with exit code 1; see the journal", `msg="tearing down failed"`)
}
