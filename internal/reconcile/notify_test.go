package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/notify"
)

type fakeNotifier struct {
	obs       []notify.Observation
	panicOn   int
	onObserve func()
}

func (f *fakeNotifier) Observe(o notify.Observation) {
	f.obs = append(f.obs, o)
	if f.onObserve != nil {
		f.onObserve()
	}
	if f.panicOn == len(f.obs) {
		panic("notifier boom")
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func findState(t *testing.T, o notify.Observation, k notify.Key) notify.State {
	t.Helper()
	for _, s := range o.States {
		if s.Key == k {
			return s
		}
	}
	t.Fatalf("no state for %+v in %+v", k, o.States)
	return notify.State{}
}

func TestRun_NotifierReceivesObservationAfterStatusWrite(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.StatusFile = filepath.Join(t.TempDir(), "status.json")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	statusExisted := false
	n := &fakeNotifier{onObserve: func() {
		_, err := os.Stat(h.r.StatusFile)
		statusExisted = err == nil
	}}
	h.r.Notifier = n

	mustNoErr(t, h.r.Run(cancelled(), nil))
	if len(n.obs) != 1 {
		t.Fatalf("observations = %d, want 1", len(n.obs))
	}
	if !statusExisted {
		t.Error("notifier ran before the status file was written")
	}
	a := findState(t, n.obs[0], notify.Key{Kind: notify.KindService, Name: "a"})
	if !a.Failing || a.Stage != "deploy" || !strings.Contains(a.Err, "deploy boom") {
		t.Errorf("state = %+v", a)
	}
	if s := findState(t, n.obs[0], notify.Key{Kind: notify.KindSync}); s.Failing {
		t.Errorf("sync state = %+v", s)
	}
}

func TestRun_NotifierPanicDoesNotAffectLoop(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.StatusFile = filepath.Join(t.TempDir(), "status.json")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	h.r.Notifier = &fakeNotifier{panicOn: 1}

	mustNoErr(t, h.r.Run(cancelled(), nil))
	if got := h.r.Tracker.Snapshot().LastCycleError; !strings.Contains(got, "deploy boom") {
		t.Errorf("LastCycleError = %q", got)
	}
	if fi, err := os.Stat(h.r.StatusFile); err != nil || fi.Size() == 0 {
		t.Errorf("status file: %v %v", fi, err)
	}
	if !strings.Contains(h.logs.String(), "notifier panicked") {
		t.Errorf("logs:\n%s", h.logs.String())
	}
}

func TestRunOnce_UnaffectedByNotifier(t *testing.T) {
	run := func(n Notifier) (string, []string) {
		h := newHarness(t, "a:apps/a:true", "b:apps/b:true")
		h.dep.deployFail["/clone/apps/a"] = errDeploy
		h.r.Notifier = n
		_, err := h.r.RunOnce(context.Background())
		return err.Error(), h.j.entries
	}
	n := &fakeNotifier{}
	e1, j1 := run(nil)
	e2, j2 := run(n)
	if e1 != e2 || !reflect.DeepEqual(j1, j2) {
		t.Errorf("with notifier: %q %v, without: %q %v", e2, j2, e1, j1)
	}
	if len(n.obs) != 0 {
		t.Error("RunOnce called the notifier")
	}
}

func TestObservation(t *testing.T) {
	h := newHarness(t, "a:apps/a:true", "off:apps/off:false", "dec:apps/dec:true")
	h.dec.failFor["dec"] = errDecrypt
	h.preloadState(t, map[string]string{"gone": "/clone/apps/gone"})
	h.syncer.steps = []syncStep{{res: result(true)}}
	_, _ = h.r.RunOnce(context.Background())

	obs := h.r.observation(h.r.Tracker.Snapshot())
	if obs.Commit != "1234567890ab" {
		t.Errorf("Commit = %q", obs.Commit)
	}
	if s := findState(t, obs, notify.Key{Kind: notify.KindService, Name: "dec"}); !s.Failing || s.Stage != "decrypt" {
		t.Errorf("dec = %+v", s)
	}
	if s := findState(t, obs, notify.Key{Kind: notify.KindService, Name: "a"}); s.Failing || s.Stage != "" {
		t.Errorf("a = %+v", s)
	}
	// Torn down successfully: healthy.
	if s := findState(t, obs, notify.Key{Kind: notify.KindService, Name: "gone"}); s.Failing {
		t.Errorf("gone = %+v", s)
	}
	// Never attempted (disabled): absent.
	for _, s := range obs.States {
		if s.Key.Name == "off" {
			t.Errorf("never-attempted service present: %+v", s)
		}
	}
}

func TestObservation_SyncErrorIsRedacted(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.Config.RepoURL = "https://user:tok@example.com/org/repo.git"
	h.syncer.steps = []syncStep{{err: errSync}}
	_, _ = h.r.RunOnce(context.Background())
	snap := h.r.Tracker.Snapshot()
	// As if the redaction where the error is created had been missed.
	snap.LastSyncError = "fetch " + h.r.Config.RepoURL + " failed"
	s := h.r.observation(snap).States[0]
	if s.Key.Kind != notify.KindSync || !s.Failing || s.Stage != "sync" {
		t.Fatalf("sync state = %+v", s)
	}
	if strings.Contains(s.Err, "tok") {
		t.Errorf("repo credentials in sync error: %q", s.Err)
	}
}

func TestObservation_StageClearedAfterSuccess(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.dep.deployFail["/clone/apps/a"] = errDeploy
	_, _ = h.cycle(t, true)
	key := notify.Key{Kind: notify.KindService, Name: "a"}
	if s := findState(t, h.r.observation(h.r.Tracker.Snapshot()), key); !s.Failing || s.Stage != "deploy" {
		t.Fatalf("state = %+v", s)
	}
	h.dep.deployFail["/clone/apps/a"] = nil
	_, err := h.cycle(t, true)
	mustNoErr(t, err)
	if s := findState(t, h.r.observation(h.r.Tracker.Snapshot()), key); s.Failing || s.Stage != "" {
		t.Errorf("state after recovery = %+v", s)
	}
}
