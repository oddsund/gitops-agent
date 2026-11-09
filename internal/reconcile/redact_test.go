package reconcile

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/notify"
	"github.com/oddsund/gitops-agent/internal/redact"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
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

// decryptAdapter lets a test choose how decrypting behaves while removal
// uses the real sopsdecrypt.
type decryptAdapter struct {
	secrets string
	dec     func(dir, name string) error
}

func (d decryptAdapter) DecryptServiceSecrets(dir, name string) error { return d.dec(dir, name) }
func (d decryptAdapter) RemoveServiceSecrets(name string) error {
	return sopsdecrypt.RemoveServiceSecrets(name, d.secrets, nil)
}

var longHex = regexp.MustCompile(`[0-9a-fA-F]{32,}`)

// assertDecryptSurfacesSafe checks every surface for a failed decrypt: only
// the fixed summary, nothing from forbidden, and a journal line carrying
// wantDetail.
func assertDecryptSurfacesSafe(t *testing.T, h *harness, n *fakeNotifier, summary, wantDetail string, forbidden ...string) {
	t.Helper()
	forbidden = append(forbidden, canary, "invalid dotenv input line", "expected mac")
	snap := h.r.Tracker.Snapshot()
	surfaces := map[string]string{"LastCycleError": snap.LastCycleError}
	for _, s := range snap.Services {
		if s.Name == "demo" {
			surfaces["LastError"] = s.LastError
			if s.LastError != summary {
				t.Errorf("LastError = %q, want %q", s.LastError, summary)
			}
		}
	}
	if !strings.Contains(snap.LastCycleError, "decrypting secrets for demo: "+summary) {
		t.Errorf("LastCycleError = %q", snap.LastCycleError)
	}
	_, surfaces["status.json"] = get(t, h.r.Tracker.Handler(), "/status.json")
	_, surfaces["html"] = get(t, h.r.Tracker.Handler(), "/")
	_, surfaces["healthz"] = get(t, h.r.Tracker.Handler(), "/healthz")
	raw, err := os.ReadFile(h.r.StatusFile)
	if err != nil {
		t.Fatal(err)
	}
	surfaces["status file"] = string(raw)
	if len(n.obs) == 0 {
		t.Fatal("no observation")
	}
	st := findState(t, n.obs[len(n.obs)-1], notify.Key{Kind: notify.KindService, Name: "demo"})
	if st.Stage != "decrypt" || st.Err != summary {
		t.Errorf("observation state = %+v", st)
	}
	surfaces["notify"] = st.Err
	surfaces["logs"] = h.logs.String()
	for name, text := range surfaces {
		// The harness commit hash is legitimately 40 hex characters.
		if longHex.MatchString(strings.ReplaceAll(text, hashAfter.String(), "")) {
			t.Errorf("%s has a long hex run: %s", name, text)
		}
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Errorf("%s contains %q: %s", name, f, text)
			}
		}
	}
	var found bool
	for _, l := range strings.Split(h.logs.String(), "\n") {
		if strings.Contains(l, `msg="decrypting secrets failed"`) {
			found = strings.Contains(l, "detail=") && strings.Contains(l, wantDetail)
			break
		}
	}
	if !found {
		t.Errorf("no decrypt failure line with detail %q in logs:\n%s", wantDetail, h.logs.String())
	}
}

func TestRun_DecryptErrorStubSafeEverywhere(t *testing.T) {
	h, n, tmp := redactHarness(t)
	const summary = "sops decrypt failed; see the journal"
	h.r.Decrypter = decryptAdapter{secrets: filepath.Join(tmp, "secrets"), dec: func(string, string) error {
		return &redact.Error{Summary: summary, Detail: "secret-d"}
	}}
	mustNoErr(t, h.r.Run(cancelled(), nil))
	assertDecryptSurfacesSafe(t, h, n, summary, "secret-d")
}

func TestRun_DecryptErrorRealSopsSafeEverywhere(t *testing.T) {
	h, n, tmp := redactHarness(t)
	secrets := filepath.Join(tmp, "secrets")
	enc := filepath.Join(h.r.Config.ClonePath, "apps", "demo", "secrets.enc.env")
	if err := os.WriteFile(enc, []byte("NOT-A-DOTENV-LINE-"+canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := filepath.Abs("../sopsdecrypt/testdata/test_key")
	if err != nil {
		t.Fatal(err)
	}
	h.r.Decrypter = decryptAdapter{secrets: secrets, dec: func(dir, name string) error {
		return sopsdecrypt.DecryptServiceSecrets(dir, name, key, secrets, nil)
	}}
	mustNoErr(t, h.r.Run(cancelled(), nil))
	assertDecryptSurfacesSafe(t, h, n, "sops decrypt failed; see the journal", "dotenv parse error (offending line not logged)", "NOT-A-DOTENV-LINE")
}

// A failure to remove the decrypted file is the agent's own error: a path
// and an errno, shown verbatim, with no file content.
func TestRun_RemoveSecretsErrorVerbatimNoContent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	h, _, tmp := redactHarness(t)
	secrets := filepath.Join(tmp, "secrets")
	h.r.Decrypter = decryptAdapter{secrets: secrets, dec: func(string, string) error { return nil }}
	mustNoErr(t, h.r.Run(cancelled(), nil))

	h.loader.cfg = svcs("demo:apps/demo:false")
	h.syncer.steps = []syncStep{{res: result(true)}}
	dir := filepath.Join(secrets, "demo")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	mustNoErr(t, h.r.Run(cancelled(), nil))

	snap := h.r.Tracker.Snapshot()
	if !strings.Contains(snap.LastCycleError, "removing secrets for demo:") || !strings.Contains(snap.LastCycleError, filepath.Join(dir, "secrets.env")) {
		t.Errorf("LastCycleError = %q, want the verbatim path error", snap.LastCycleError)
	}
	if strings.Contains(snap.LastCycleError, canary) {
		t.Errorf("LastCycleError leaks the canary: %q", snap.LastCycleError)
	}
}

// The remove-secrets logs carry detail= when the error has one.
func TestRun_RemoveSecretsLogsDetail(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:false")
	h.dec.removeFail["b"] = &redact.Error{Summary: "remove failed; see the journal", Detail: "detail-b"}
	_, _ = h.cycle(t, true)
	var n int
	for _, l := range strings.Split(h.logs.String(), "\n") {
		if strings.Contains(l, `msg="removing decrypted secrets failed"`) {
			n++
			if !strings.Contains(l, "detail=detail-b") {
				t.Errorf("no detail in %q", l)
			}
		}
	}
	if n == 0 {
		t.Errorf("no removal failure logged:\n%s", h.logs.String())
	}
}
