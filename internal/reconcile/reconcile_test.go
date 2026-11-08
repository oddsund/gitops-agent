package reconcile

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/state"
	"github.com/oddsund/gitops-agent/internal/statusserver"
)

var (
	errSync    = errors.New("sync boom")
	errDecrypt = errors.New("decrypt boom")
	errDeploy  = errors.New("deploy boom")
	errDown    = errors.New("down boom")
	errChange  = errors.New("change boom")
	errLoad    = errors.New("load boom")
	errRemove  = errors.New("remove boom")
)

var (
	hashBefore = plumbing.NewHash("abcdef1234567890abcdef1234567890abcdef12")
	hashAfter  = plumbing.NewHash("1234567890abcdef1234567890abcdef12345678")
)

// journal is shared by the decrypter and deployer fakes so tests can assert
// the order of calls across collaborators.
type journal struct{ entries []string }

func (j *journal) add(s string) { j.entries = append(j.entries, s) }
func (j *journal) reset()       { j.entries = nil }
func (j *journal) index(s string) int {
	for i, e := range j.entries {
		if e == s {
			return i
		}
	}
	return -1
}

type syncStep struct {
	res gitsync.Result
	err error
}

type fakeSyncer struct {
	steps  []syncStep
	calls  int
	onCall func(n int) // called with the 1-based call number before returning
}

func (f *fakeSyncer) Sync() (gitsync.Result, error) {
	f.calls++
	if f.onCall != nil {
		f.onCall(f.calls)
	}
	i := f.calls - 1
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	return f.steps[i].res, f.steps[i].err
}

type fakeChanges struct {
	changed map[string]bool
	err     error
	paths   []string
	results []gitsync.Result
}

func (f *fakeChanges) PathChanged(res gitsync.Result, path string) (bool, error) {
	f.paths = append(f.paths, path)
	f.results = append(f.results, res)
	if f.err != nil {
		return false, f.err
	}
	return f.changed[path], nil
}

type fakeLoader struct {
	cfg   *config.ServicesConfig
	err   error
	paths []string
}

func (f *fakeLoader) LoadServices(path string) (*config.ServicesConfig, error) {
	f.paths = append(f.paths, path)
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

type fakeDecrypter struct {
	j          *journal
	failFor    map[string]error
	removeFail map[string]error
	removed    []string       // every RemoveServiceSecrets call, in order
	removedAt  map[string]int // len(j.entries) when the name was last removed
}

// RemoveServiceSecrets deliberately does not write to the journal so the
// exact-journal assertions elsewhere stay unchanged.
func (f *fakeDecrypter) RemoveServiceSecrets(name string) error {
	f.removed = append(f.removed, name)
	if f.removedAt == nil {
		f.removedAt = map[string]int{}
	}
	f.removedAt[name] = len(f.j.entries)
	return f.removeFail[name]
}

func (f *fakeDecrypter) DecryptServiceSecrets(serviceDir, serviceName string) error {
	f.j.add("decrypt:" + serviceName)
	return f.failFor[serviceName]
}

type fakeDeployer struct {
	j          *journal
	deployFail map[string]error // keyed by service dir
	downFail   map[string]error // keyed by service dir
}

func (f *fakeDeployer) Deploy(dir string) error {
	f.j.add("deploy:" + dir)
	return f.deployFail[dir]
}

func (f *fakeDeployer) Down(dir string) error {
	f.j.add("down:" + dir)
	return f.downFail[dir]
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

type harness struct {
	r       *Reconciler
	syncer  *fakeSyncer
	changes *fakeChanges
	loader  *fakeLoader
	dec     *fakeDecrypter
	dep     *fakeDeployer
	j       *journal
	clock   *fakeClock
	logs    *bytes.Buffer
}

func result(changed bool) gitsync.Result {
	return gitsync.Result{Changed: changed, Before: hashBefore, After: hashAfter}
}

// svcs parses "name:path:enabled" triples.
func svcs(specs ...string) *config.ServicesConfig {
	out := &config.ServicesConfig{}
	for _, s := range specs {
		p := strings.Split(s, ":")
		out.Services = append(out.Services, config.Service{Name: p[0], Path: p[1], Enabled: p[2] == "true"})
	}
	return out
}

func newHarness(t *testing.T, manifest ...string) *harness {
	t.Helper()
	j := &journal{}
	lg, logs := newTestLogger()
	h := &harness{
		logs:    logs,
		syncer:  &fakeSyncer{steps: []syncStep{{res: result(false)}}},
		changes: &fakeChanges{changed: map[string]bool{}},
		loader:  &fakeLoader{cfg: svcs(manifest...)},
		dec:     &fakeDecrypter{j: j, failFor: map[string]error{}, removeFail: map[string]error{}},
		dep:     &fakeDeployer{j: j, deployFail: map[string]error{}, downFail: map[string]error{}},
		j:       j,
		clock:   &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
	}
	h.r = &Reconciler{
		Config: Config{
			RepoURL:            "git@example.com:org/repo.git",
			Branch:             "main",
			ClonePath:          "/clone",
			StatePath:          filepath.Join(t.TempDir(), "state.json"),
			Idle:               time.Hour,
			Active:             15 * time.Second,
			ActiveWindow:       10 * time.Minute,
			FullReconcileEvery: time.Hour,
		},
		Syncer:     h.syncer,
		Changes:    h.changes,
		Services:   h.loader,
		Decrypter:  h.dec,
		Deployer:   h.dep,
		Tracker:    statusserver.NewTracker("test", time.Time{}),
		Now:        h.clock.now,
		StatusFile: "",
		Log:        lg,
	}
	return h
}

// cycle runs one RunOnce with the sync reporting changed.
func (h *harness) cycle(t *testing.T, changed bool) (bool, error) {
	t.Helper()
	h.syncer.steps = []syncStep{{res: result(changed)}}
	h.syncer.calls = 0
	return h.r.RunOnce(context.Background())
}

func (h *harness) preloadState(t *testing.T, st state.State) {
	t.Helper()
	if err := state.Save(h.r.Config.StatePath, st); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) state() state.State { return state.Load(h.r.Config.StatePath, nil) }

func svcStatus(t *testing.T, tr *statusserver.Tracker, name string) statusserver.ServiceStatus {
	t.Helper()
	for _, s := range tr.Snapshot().Services {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("service %q not in tracker snapshot", name)
	return statusserver.ServiceStatus{}
}

// newTestLogger returns a logger writing logfmt to buf with no time attr,
// at Debug so tests see every level.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(g []string, a slog.Attr) slog.Attr {
			if len(g) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(h), &buf
}

func assertJournal(t *testing.T, h *harness, want ...string) {
	t.Helper()
	got := h.j.entries
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("journal = %v, want %v", got, want)
	}
}

func assertState(t *testing.T, h *harness, want state.State) {
	t.Helper()
	if got := h.state(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %v, want %v", got, want)
	}
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOnce_SyncError(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.syncer.steps = []syncStep{{err: errSync}}

	changed, err := h.r.RunOnce(context.Background())
	if changed {
		t.Error("changed = true, want false")
	}
	if !errors.Is(err, errSync) {
		t.Fatalf("err = %v, want errSync", err)
	}
	if !strings.HasPrefix(err.Error(), "syncing git@example.com:org/repo.git: ") {
		t.Errorf("err = %q, want syncing prefix", err)
	}
	st := h.r.Tracker.Snapshot()
	if st.LastSyncError == "" || st.CommitHash != "" {
		t.Errorf("tracker sync error = %q hash = %q", st.LastSyncError, st.CommitHash)
	}
	if len(h.loader.paths) != 0 || len(h.j.entries) != 0 {
		t.Errorf("later stages ran: loader %v journal %v", h.loader.paths, h.j.entries)
	}
	if _, statErr := os.Stat(h.r.Config.StatePath); !os.IsNotExist(statErr) {
		t.Errorf("state file exists after sync failure: %v", statErr)
	}
}

func TestRunOnce_SyncSuccessRecordsCommit(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	st := h.r.Tracker.Snapshot()
	if st.CommitHash != hashAfter.String() || st.LastSyncError != "" {
		t.Errorf("hash = %q err = %q", st.CommitHash, st.LastSyncError)
	}
}

func TestRunOnce_LoadsManifestFromClone(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	want := []string{filepath.Join("/clone", "services.toml")}
	if !reflect.DeepEqual(h.loader.paths, want) {
		t.Errorf("loader paths = %v, want %v", h.loader.paths, want)
	}
}

func TestRunOnce_NoManifestFirstCycle_DeploysNothing(t *testing.T) {
	h := newHarness(t)
	h.loader.cfg, h.loader.err = nil, errLoad
	pre := state.State{"old": "/clone/old"}
	h.preloadState(t, pre)

	changed, err := h.cycle(t, true)
	mustNoErr(t, err)
	if !changed {
		t.Error("changed = false, want true")
	}
	assertJournal(t, h)
	if n := len(h.r.Tracker.Snapshot().Services); n != 0 {
		t.Errorf("tracker has %d services, want 0", n)
	}
	assertState(t, h, pre)
	if out := h.logs.String(); !strings.Contains(out, "nothing to deploy") || !strings.Contains(out, "level=WARN") {
		t.Errorf("log lacks a WARN 'nothing to deploy': %q", out)
	}
}

func TestRunOnce_BrokenManifestKeepsLastKnownGood(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.loader.cfg, h.loader.err = nil, errLoad
	h.clock.advance(h.r.Config.FullReconcileEvery)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	if got := h.r.Tracker.Snapshot().ManifestError; !strings.Contains(got, errLoad.Error()) {
		t.Errorf("ManifestError = %q", got)
	}
	if out := h.logs.String(); !strings.Contains(out, "keeping last-known-good") || !strings.Contains(out, "level=WARN") || !strings.Contains(out, "path=") {
		t.Errorf("log lacks a WARN 'keeping last-known-good' with path: %q", out)
	}
}

func TestRunOnce_ManifestRecovers(t *testing.T) {
	h := newHarness(t)
	h.loader.cfg, h.loader.err = nil, errLoad
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)

	h.loader.cfg, h.loader.err = svcs("a:apps/a:true"), nil
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

func TestRunOnce_TracksDisabledServices(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:false")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	a := svcStatus(t, h.r.Tracker, "a")
	b := svcStatus(t, h.r.Tracker, "b")
	if !a.Enabled || a.Path != "apps/a" {
		t.Errorf("a = %+v", a)
	}
	if b.Enabled || b.Path != "apps/b" {
		t.Errorf("b = %+v", b)
	}
	if !b.LastAttempt.IsZero() {
		t.Errorf("disabled b was attempted: %v", b.LastAttempt)
	}
}

func TestRunOnce_FirstCycleDeploysAllEnabled(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true", "c:apps/c:false")
	changed, err := h.cycle(t, false)
	mustNoErr(t, err)
	if changed {
		t.Error("changed = true, want false")
	}
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	for _, n := range []string{"a", "b"} {
		if s := svcStatus(t, h.r.Tracker, n); !s.LastAttemptOK || s.LastError != "" {
			t.Errorf("%s status = %+v", n, s)
		}
	}
	assertState(t, h, state.State{"a": "/clone/apps/a", "b": "/clone/apps/b"})
	if len(h.changes.paths) != 0 {
		t.Errorf("detector called on a full reconcile: %v", h.changes.paths)
	}
}

func TestRunOnce_DecryptFailureIsolated(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dec.failFor["a"] = errDecrypt
	_, err := h.cycle(t, false)
	if !errors.Is(err, errDecrypt) || !strings.Contains(err.Error(), "decrypting secrets for a") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, h, "decrypt:a", "decrypt:b", "deploy:/clone/apps/b")
	a := svcStatus(t, h.r.Tracker, "a")
	if a.LastAttemptOK || a.LastError == "" {
		t.Errorf("a = %+v", a)
	}
	if !svcStatus(t, h.r.Tracker, "b").LastAttemptOK {
		t.Error("b not OK")
	}
	assertState(t, h, state.State{"b": "/clone/apps/b"})
}

func TestRunOnce_DecryptFailureRetriedNextCycle(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dec.failFor["a"] = errDecrypt
	_, _ = h.cycle(t, false)

	h.dec.failFor["a"] = nil
	h.j.reset()
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

// A service that deployed once is normally skipped when nothing changed. A
// decrypt failure (for example a stale secrets.env that could not be
// removed) must not leave it in that state, or nothing retries it.
func TestRunOnce_DecryptFailureRetriedAfterSuccessfulDeploy(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.dec.failFor["a"] = errDecrypt
	h.changes.changed["apps/a"] = true
	h.j.reset()
	_, err = h.cycle(t, true)
	if !errors.Is(err, errDecrypt) {
		t.Fatalf("err = %v, want errDecrypt", err)
	}
	assertJournal(t, h, "decrypt:a")

	h.dec.failFor["a"] = nil
	h.changes.changed["apps/a"] = false
	h.j.reset()
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

func TestRunOnce_DeployFailureIsolated(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	_, err := h.cycle(t, false)
	if !errors.Is(err, errDeploy) || !strings.Contains(err.Error(), "deploying a") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	if svcStatus(t, h.r.Tracker, "a").LastAttemptOK {
		t.Error("a reported OK")
	}
	assertState(t, h, state.State{"b": "/clone/apps/b"})
}

func TestRunOnce_JoinsAllErrors(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true", "c:apps/c:true")
	h.dec.failFor["a"] = errDecrypt
	h.dep.deployFail["/clone/apps/b"] = errDeploy
	_, err := h.cycle(t, false)
	if !errors.Is(err, errDecrypt) || !errors.Is(err, errDeploy) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "decrypting secrets for a") || !strings.Contains(err.Error(), "deploying b") {
		t.Errorf("err = %q", err)
	}
	if h.j.index("deploy:/clone/apps/c") < 0 {
		t.Error("c not deployed")
	}
}

func TestRunOnce_SkipsUpToDateServices(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	before := svcStatus(t, h.r.Tracker, "a").LastAttempt
	if strings.Contains(h.logs.String(), "cycle complete") {
		t.Errorf("summary logged with nothing skipped: %q", h.logs.String())
	}

	h.j.reset()
	h.logs.Reset()
	changed, err := h.cycle(t, false)
	mustNoErr(t, err)
	if changed {
		t.Error("changed = true")
	}
	assertJournal(t, h)
	if got := svcStatus(t, h.r.Tracker, "a").LastAttempt; !got.Equal(before) {
		t.Errorf("ServiceAttempt called for skipped service")
	}
	if len(h.changes.paths) != 0 {
		t.Errorf("detector called: %v", h.changes.paths)
	}
	if out := h.logs.String(); !strings.Contains(out, "deployed=0") || !strings.Contains(out, "up_to_date=2") {
		t.Errorf("log = %q", h.logs.String())
	}
}

func TestRunOnce_ChangedSyncDeploysOnlyTouchedServices(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.changes.changed = map[string]bool{"apps/a": true, "apps/b": false}
	changed, err := h.cycle(t, true)
	mustNoErr(t, err)
	if !changed {
		t.Error("changed = false")
	}
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
	if !reflect.DeepEqual(h.changes.paths, []string{"apps/a", "apps/b"}) {
		t.Errorf("paths = %v", h.changes.paths)
	}
	for _, r := range h.changes.results {
		if r != result(true) {
			t.Errorf("detector got %+v", r)
		}
	}
}

func TestRunOnce_PathChangedErrorFailsOpen(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.changes.err = errChange
	_, err = h.cycle(t, true)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
}

func TestRunOnce_ChangeDetectorSkippedForNeverDeployedService(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	_, _ = h.cycle(t, false)

	h.dep.deployFail["/clone/apps/a"] = nil
	h.j.reset()
	_, err := h.cycle(t, true)
	mustNoErr(t, err)
	// a has never deployed OK, so it deploys without asking the detector.
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
	if !reflect.DeepEqual(h.changes.paths, []string{"apps/b"}) {
		t.Errorf("detector paths = %v, want [apps/b]", h.changes.paths)
	}
}

func TestRunOnce_FullReconcileTimer(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	const full = "periodic full reconcile"

	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if !strings.Contains(h.logs.String(), full) {
		t.Error("cycle 1 not a full reconcile")
	}

	h.j.reset()
	h.logs.Reset()
	h.clock.advance(time.Hour - time.Second)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)
	if strings.Contains(h.logs.String(), full) {
		t.Error("cycle 2 was a full reconcile")
	}

	h.logs.Reset()
	h.clock.advance(time.Second)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	if !strings.Contains(h.logs.String(), full) {
		t.Error("cycle 3 not a full reconcile")
	}
}

func TestRunOnce_FullReconcileBoundaryIsInclusive(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	h.j.reset()
	h.clock.advance(h.r.Config.FullReconcileEvery)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

func TestRunOnce_FullReconcileTimerResetEvenWhenServiceFails(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	_, err := h.cycle(t, false)
	if err == nil {
		t.Fatal("want error")
	}
	h.dep.deployFail["/clone/apps/a"] = nil
	h.j.reset()
	h.clock.advance(time.Minute)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	// a never deployed OK so it is retried; b is skipped, proving the
	// second cycle was not a full one.
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

func TestRunOnce_DisabledServiceIsTornDown(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "down:/clone/apps/b")
	assertState(t, h, state.State{"a": "/clone/apps/a"})
	if s := svcStatus(t, h.r.Tracker, "b"); !s.LastAttemptOK || s.Enabled {
		t.Errorf("b = %+v", s)
	}
}

func TestRunOnce_ReEnabledServiceRedeploysWithoutChange(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:b", "deploy:/clone/apps/b")
}

func TestRunOnce_ServiceRemovedFromManifestIsTornDown(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "down:/clone/apps/b")
	assertState(t, h, state.State{"a": "/clone/apps/a"})
}

func TestRunOnce_TeardownRunsBeforeDeploy(t *testing.T) {
	h := newHarness(t, "new:apps/new:true")
	h.preloadState(t, state.State{"old": "/clone/apps/old"})
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	down := h.j.index("down:/clone/apps/old")
	dec := h.j.index("decrypt:new")
	dep := h.j.index("deploy:/clone/apps/new")
	if down < 0 || dec < 0 || dep < 0 || down > dec || down > dep {
		t.Fatalf("journal = %v", h.j.entries)
	}
}

func TestRunOnce_TeardownUsesRecordedDir(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.preloadState(t, state.State{"b": "/elsewhere/b"})
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if h.j.index("down:/elsewhere/b") < 0 {
		t.Fatalf("journal = %v", h.j.entries)
	}
}

func TestRunOnce_DownFailureKeepsServiceInState(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	h.dep.downFail["/clone/apps/b"] = errDown
	h.changes.changed = map[string]bool{"apps/a": true}
	_, err = h.cycle(t, true)
	if !errors.Is(err, errDown) || !strings.Contains(err.Error(), "tearing down b") {
		t.Fatalf("err = %v", err)
	}
	assertState(t, h, state.State{"a": "/clone/apps/a", "b": "/clone/apps/b"})
	if s := svcStatus(t, h.r.Tracker, "b"); s.LastAttemptOK || s.LastError == "" {
		t.Errorf("b = %+v", s)
	}
	if h.j.index("deploy:/clone/apps/a") < 0 {
		t.Errorf("a not deployed: %v", h.j.entries)
	}

	h.j.reset()
	_, err = h.cycle(t, false)
	if !errors.Is(err, errDown) {
		t.Fatalf("cycle 3 err = %v", err)
	}
	assertJournal(t, h, "down:/clone/apps/b")

	h.dep.downFail["/clone/apps/b"] = nil
	h.j.reset()
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "down:/clone/apps/b")
	assertState(t, h, state.State{"a": "/clone/apps/a"})
}

func TestRunOnce_ReEnableAfterFailedDownDoesNotRedeploy(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	h.dep.downFail["/clone/apps/b"] = errDown
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)
}

func TestRunOnce_LoadsPersistedStateOnFirstCycle(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.preloadState(t, state.State{"ghost": "/clone/apps/ghost"})
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "down:/clone/apps/ghost", "decrypt:a", "deploy:/clone/apps/a")
	assertState(t, h, state.State{"a": "/clone/apps/a"})
}

func TestRunOnce_StateSavedOnlyWhenDirty(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if err := os.Remove(h.r.Config.StatePath); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Hour)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	if h.j.index("deploy:/clone/apps/a") < 0 {
		t.Fatal("full reconcile did not redeploy")
	}
	if _, statErr := os.Stat(h.r.Config.StatePath); !os.IsNotExist(statErr) {
		t.Errorf("state rewritten although nothing changed: %v", statErr)
	}
}

func TestRunOnce_StateDirUpdatedWhenPathMoves(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.loader.cfg = svcs("a:apps/a2:true")
	h.changes.changed = map[string]bool{"apps/a2": true}
	_, err := h.cycle(t, true)
	mustNoErr(t, err)
	assertState(t, h, state.State{"a": "/clone/apps/a2"})
	if h.j.index("down:/clone/apps/a") >= 0 {
		t.Errorf("old dir torn down: %v", h.j.entries)
	}
}

func TestRunOnce_StateSaveFailureIsJoined(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.r.Config.StatePath = filepath.Join(blocker, "state.json")
	h.dec.failFor["b"] = errDecrypt

	_, err := h.cycle(t, false)
	if err == nil || !strings.Contains(err.Error(), "saving deploy state") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, errDecrypt) {
		t.Errorf("decrypt error lost: %v", err)
	}
	if h.j.index("deploy:/clone/apps/a") < 0 {
		t.Error("a not deployed")
	}
}

func TestRunOnce_ReturnsChangedFlag(t *testing.T) {
	for _, changed := range []bool{true, false} {
		for _, fail := range []bool{true, false} {
			h := newHarness(t, "a:apps/a:true")
			if fail {
				h.dep.deployFail["/clone/apps/a"] = errDeploy
			}
			got, err := h.cycle(t, changed)
			if got != changed {
				t.Errorf("changed=%v fail=%v: got %v", changed, fail, got)
			}
			if (err != nil) != fail {
				t.Errorf("changed=%v fail=%v: err %v", changed, fail, err)
			}
		}
	}
}

func TestRun_RunsOneCycleThenStopsOnCancel(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.StatusFile = filepath.Join(t.TempDir(), "status.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mustNoErr(t, h.r.Run(ctx, nil))
	if h.syncer.calls != 1 {
		t.Errorf("sync calls = %d, want 1", h.syncer.calls)
	}
	st := h.r.Tracker.Snapshot()
	if st.LastCycleAt.IsZero() {
		t.Error("CycleComplete not called")
	}
	d := st.NextCycleAt.Sub(h.clock.now())
	if d < 54*time.Minute || d > 66*time.Minute {
		t.Errorf("next cycle in %v, want about 1h", d)
	}
	if fi, err := os.Stat(h.r.StatusFile); err != nil || fi.Size() == 0 {
		t.Errorf("status file: %v %v", fi, err)
	}
}

func TestRun_CycleErrorRecordedAndLoopContinues(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.syncer.steps = []syncStep{{err: errSync}}
	h.syncer.onCall = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	reload := make(chan struct{}, 1)
	reload <- struct{}{}

	mustNoErr(t, h.r.Run(ctx, reload))
	if h.syncer.calls != 2 {
		t.Errorf("sync calls = %d, want 2", h.syncer.calls)
	}
	if got := h.r.Tracker.Snapshot().LastCycleError; !strings.Contains(got, "syncing") {
		t.Errorf("LastCycleError = %q", got)
	}
}

func TestRun_EmptyStatusFileSkipsWrite(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	t.Chdir(dir)
	mustNoErr(t, h.r.Run(ctx, nil))
	entries, err := os.ReadDir(dir)
	mustNoErr(t, err)
	if len(entries) != 0 {
		t.Errorf("files written: %v", entries)
	}
}

func TestRun_ActiveTransitionLogged(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.syncer.steps = []syncStep{{res: result(true)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mustNoErr(t, h.r.Run(ctx, nil))
	if !strings.Contains(h.logs.String(), "new commits") || !strings.Contains(h.logs.String(), "interval=15s") {
		t.Errorf("log = %q", h.logs.String())
	}
}

func assertRemoved(t *testing.T, h *harness, want ...string) {
	t.Helper()
	if strings.Join(h.dec.removed, ",") != strings.Join(want, ",") {
		t.Fatalf("removed = %v, want %v", h.dec.removed, want)
	}
}

func TestRunOnce_RemovesSecretsAfterTeardown(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h)

	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h, "b")
	if h.dec.removedAt["b"] <= h.j.index("down:/clone/apps/b") {
		t.Errorf("secrets removed before down: %v at %d", h.j.entries, h.dec.removedAt["b"])
	}
}

func TestRunOnce_RemovesSecretsForServiceDeletedFromManifest(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)

	h.loader.cfg = svcs("a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h, "b")
}

func TestRunOnce_DownFailureKeepsSecrets(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)

	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	h.dep.downFail["/clone/apps/b"] = errDown
	for i := 0; i < 3; i++ {
		_, err := h.cycle(t, false)
		if !errors.Is(err, errDown) {
			t.Fatalf("cycle %d err = %v", i, err)
		}
	}
	assertRemoved(t, h)

	h.dep.downFail["/clone/apps/b"] = nil
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h, "b")
}

func TestRunOnce_DisabledServiceSecretsRemovedEveryCycle(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "c:apps/c:false")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	h.changes.changed = map[string]bool{"apps/a": true}
	_, err = h.cycle(t, true)
	mustNoErr(t, err)
	h.clock.advance(2 * time.Hour)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h, "c", "c", "c")
}

func TestRunOnce_EnabledServiceKeepsSecretsOnFailure(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	h.dec.failFor["b"] = errDecrypt
	_, err := h.cycle(t, false)
	if !errors.Is(err, errDeploy) || !errors.Is(err, errDecrypt) {
		t.Fatalf("err = %v", err)
	}
	assertRemoved(t, h)
}

func TestRunOnce_SecretsRemovalFailureIsolated(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, _ = h.cycle(t, false)

	h.dec.removeFail["b"] = errRemove
	h.loader.cfg = svcs("a:apps/a:true", "b:apps/b:false")
	h.changes.changed = map[string]bool{"apps/a": true}
	h.j.reset()
	_, err := h.cycle(t, true)
	if !errors.Is(err, errRemove) || !strings.Contains(err.Error(), "removing secrets for b") {
		t.Fatalf("err = %v", err)
	}
	if h.j.index("deploy:/clone/apps/a") < 0 {
		t.Errorf("a not deployed: %v", h.j.entries)
	}
	assertState(t, h, state.State{"a": "/clone/apps/a"})
	if s := svcStatus(t, h.r.Tracker, "b"); !s.LastAttemptOK {
		t.Errorf("b = %+v", s)
	}

	h.dec.removed = nil
	_, err = h.cycle(t, false)
	if !errors.Is(err, errRemove) {
		t.Fatalf("retry err = %v", err)
	}
	assertRemoved(t, h, "b")

	h.dec.removeFail["b"] = nil
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
}

func TestRunOnce_NoManifestNoRemoval(t *testing.T) {
	h := newHarness(t)
	h.loader.cfg = nil
	h.loader.err = errLoad
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h)
}

func TestRunOnce_RenamedServiceOnlyOldNameRemoved(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.loader.cfg = svcs("a2:apps/a2:true")
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h, "a")
	assertState(t, h, state.State{"a2": "/clone/apps/a2"})
}

func TestRunOnce_ReEnabledServiceNotSweptAndRedecrypted(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, _ = h.cycle(t, false)
	h.loader.cfg = svcs("a:apps/a:false")
	_, _ = h.cycle(t, false)
	assertRemoved(t, h, "a")

	h.dec.removed = nil
	h.j.reset()
	h.loader.cfg = svcs("a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertRemoved(t, h)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a")
}

func TestRunOnce_FailureLogsErrorWithService(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		msg   string
		errAs string
	}{
		{"deploy", func(h *harness) { h.dep.deployFail["/clone/apps/a"] = errDeploy }, "deploy failed", `error="deploy boom"`},
		{"decrypt", func(h *harness) { h.dec.failFor["a"] = errDecrypt }, "decrypting secrets failed", `error="decrypt boom"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "a:apps/a:true")
			tc.setup(h)
			if _, err := h.cycle(t, true); err == nil {
				t.Fatal("err = nil, want failure")
			}
			assertLogLine(t, h, `msg="`+tc.msg+`"`, "level=ERROR", "service=a", "cycle=1", tc.errAs)
		})
	}
}

func TestRunOnce_TeardownFailureLogsErrorWithService(t *testing.T) {
	h := newHarness(t, "b:apps/b:true")
	h.preloadState(t, state.State{"b": "/clone/apps/b"})
	h.loader.cfg = svcs("b:apps/b:false")
	h.dep.downFail["/clone/apps/b"] = errDown
	if _, err := h.cycle(t, true); err == nil {
		t.Fatal("err = nil, want failure")
	}
	assertLogLine(t, h, `msg="tearing down failed"`, "level=ERROR", "service=b", "cycle=1", `error="down boom"`)
}

// assertLogLine finds the line holding msg and checks every want on that one
// line, so the attrs cannot come from different lines.
func assertLogLine(t *testing.T, h *harness, msg string, want ...string) {
	t.Helper()
	for _, line := range strings.Split(h.logs.String(), "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		for _, w := range want {
			if !strings.Contains(line, w) {
				t.Errorf("line %q lacks %q", line, w)
			}
		}
		return
	}
	t.Errorf("no log line with %s in:\n%s", msg, h.logs.String())
}

func TestRunOnce_CycleAttrIncrements(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if !strings.Contains(h.logs.String(), "cycle=1") || strings.Contains(h.logs.String(), "cycle=2") {
		t.Errorf("first cycle log = %q", h.logs.String())
	}
	h.logs.Reset()
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertLogLine(t, h, `msg="cycle complete"`, "cycle=2")
}

func TestRunOnce_NilLogDiscards(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.Log = nil
	_, err := h.cycle(t, true)
	mustNoErr(t, err)
}

func TestRunOnce_OtherFailureRowsLogLevelServiceAndCycle(t *testing.T) {
	t.Run("teardown secrets removal", func(t *testing.T) {
		h := newHarness(t, "b:apps/b:false")
		h.preloadState(t, state.State{"b": "/clone/apps/b"})
		h.dec.removeFail["b"] = errRemove
		_, _ = h.cycle(t, true)
		assertLogLine(t, h, `msg="removing decrypted secrets failed"`, "level=ERROR", "service=b", "cycle=1", `error="remove boom"`)
	})
	t.Run("disabled sweep secrets removal", func(t *testing.T) {
		h := newHarness(t, "c:apps/c:false")
		h.dec.removeFail["c"] = errRemove
		_, _ = h.cycle(t, true)
		assertLogLine(t, h, `msg="removing decrypted secrets failed"`, "level=ERROR", "service=c", "cycle=1", `error="remove boom"`)
	})
	t.Run("state save", func(t *testing.T) {
		h := newHarness(t, "a:apps/a:true")
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		h.r.Config.StatePath = filepath.Join(blocker, "state.json")
		_, _ = h.cycle(t, false)
		assertLogLine(t, h, `msg="saving deploy state failed"`, "level=ERROR", "cycle=1", "path=")
	})
	t.Run("path changed warn", func(t *testing.T) {
		h := newHarness(t, "a:apps/a:true")
		_, err := h.cycle(t, false)
		mustNoErr(t, err)
		h.logs.Reset()
		h.changes.err = errChange
		_, err = h.cycle(t, true)
		mustNoErr(t, err)
		assertLogLine(t, h, "couldn't tell whether the service changed", "level=WARN", "service=a", "cycle=2", `error="change boom"`)
	})
}

func TestServiceNeedsDeploy_NilLoggerDoesNotPanic(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.Log = nil
	h.changes.err = errChange
	if !h.r.serviceNeedsDeploy(result(true), config.Service{Name: "a", Path: "apps/a"}) {
		t.Error("want fail-open true")
	}
}
