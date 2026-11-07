package reconcile

import (
	"bytes"
	"context"
	"errors"
	"log"
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
	j       *journal
	failFor map[string]error
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
	h := &harness{
		syncer:  &fakeSyncer{steps: []syncStep{{res: result(false)}}},
		changes: &fakeChanges{changed: map[string]bool{}},
		loader:  &fakeLoader{cfg: svcs(manifest...)},
		dec:     &fakeDecrypter{j: j, failFor: map[string]error{}},
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

func (h *harness) state() state.State { return state.Load(h.r.Config.StatePath) }

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

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return &buf
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
	buf := captureLog(t)
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
	if !strings.Contains(buf.String(), "nothing to deploy") {
		t.Errorf("log lacks 'nothing to deploy': %q", buf.String())
	}
}

func TestRunOnce_BrokenManifestKeepsLastKnownGood(t *testing.T) {
	buf := captureLog(t)
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)

	h.j.reset()
	h.loader.cfg, h.loader.err = nil, errLoad
	h.clock.advance(h.r.Config.FullReconcileEvery)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	if !strings.Contains(buf.String(), "keeping last-known-good") {
		t.Errorf("log lacks 'keeping last-known-good': %q", buf.String())
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
	buf := captureLog(t)
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	before := svcStatus(t, h.r.Tracker, "a").LastAttempt
	if strings.Contains(buf.String(), "cycle complete") {
		t.Errorf("summary logged with nothing skipped: %q", buf.String())
	}

	h.j.reset()
	buf.Reset()
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
	if !strings.Contains(buf.String(), "0 deployed, 2 already up to date") {
		t.Errorf("log = %q", buf.String())
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
	buf := captureLog(t)
	h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
	const full = "periodic full reconcile"

	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if !strings.Contains(buf.String(), full) {
		t.Error("cycle 1 not a full reconcile")
	}

	h.j.reset()
	buf.Reset()
	h.clock.advance(time.Hour - time.Second)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)
	if strings.Contains(buf.String(), full) {
		t.Error("cycle 2 was a full reconcile")
	}

	buf.Reset()
	h.clock.advance(time.Second)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a", "decrypt:b", "deploy:/clone/apps/b")
	if !strings.Contains(buf.String(), full) {
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
	buf := captureLog(t)
	h := newHarness(t, "a:apps/a:true")
	h.syncer.steps = []syncStep{{res: result(true)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mustNoErr(t, h.r.Run(ctx, nil))
	if !strings.Contains(buf.String(), "new commits: reconciling every") {
		t.Errorf("log = %q", buf.String())
	}
}
