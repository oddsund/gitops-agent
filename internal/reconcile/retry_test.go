package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/oddsund/gitops-agent/internal/notify"
)

const aDir = "/clone/apps/a"

// failedAfterSuccess deploys a once, then fails a redeploy triggered by a
// changed commit. The clock stands where the failure happened and a has one
// recorded failure.
func failedAfterSuccess(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	h.dep.deployFail[aDir] = errDeploy
	h.changes.changed["apps/a"] = true
	if _, err := h.cycle(t, true); err == nil {
		t.Fatal("want deploy error")
	}
	h.changes.changed["apps/a"] = false
	h.j.reset()
	return h
}

func deployCalls(h *harness) int {
	n := 0
	for _, e := range h.j.entries {
		if e == "deploy:"+aDir {
			n++
		}
	}
	return n
}

func TestRetryDelay(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	want := []time.Duration{30, 60, 120, 240, 480, 600, 600}
	for i, w := range want {
		if got := h.r.retryDelay(i + 1); got != w*time.Second {
			t.Errorf("failures=%d: delay %v, want %v", i+1, got, w*time.Second)
		}
	}
	for _, n := range []int{21, 63, 64, 1000} {
		if got := h.r.retryDelay(n); got != 10*time.Minute {
			t.Errorf("failures=%d: delay %v, want the cap", n, got)
		}
	}
}

func TestRetryDelay_CappedByFullReconcileInterval(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.r.Config.FullReconcileEvery = 2 * time.Minute
	for _, n := range []int{4, 5, 6, 30} {
		if got := h.r.retryDelay(n); got != 2*time.Minute {
			t.Errorf("failures=%d: delay %v, want 2m", n, got)
		}
	}
}

func TestRetry_NotBeforeBackoff(t *testing.T) {
	h := failedAfterSuccess(t)
	h.clock.advance(10 * time.Second)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)
}

func TestRetry_AfterBackoffDoublesDelay(t *testing.T) {
	h := failedAfterSuccess(t)
	for _, d := range []time.Duration{60, 120, 240, 480, 600, 600} {
		h.clock.advance(h.r.retry["a"].next.Sub(h.clock.now()) - time.Second)
		h.j.reset()
		if _, err := h.cycle(t, false); err != nil || deployCalls(h) != 0 {
			t.Fatalf("retried one second early: err=%v journal=%v", err, h.j.entries)
		}
		h.clock.advance(time.Second)
		if _, err := h.cycle(t, false); err == nil || deployCalls(h) != 1 {
			t.Fatalf("not retried when due: err=%v journal=%v", err, h.j.entries)
		}
		if got := h.r.retry["a"].next.Sub(h.clock.now()); got != d*time.Second {
			t.Fatalf("next delay = %v, want %v", got, d*time.Second)
		}
	}
}

func TestRetry_SuccessClearsState(t *testing.T) {
	h := failedAfterSuccess(t)
	h.dep.deployFail[aDir] = nil
	h.clock.advance(retryBase)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
	if _, ok := h.r.retry["a"]; ok {
		t.Error("retry state kept after success")
	}
	if !h.r.deployedOnce["a"] {
		t.Error("deployedOnce not set after success")
	}
	if !svcStatus(t, h.r.Tracker, "a").LastAttemptOK {
		t.Error("tracker not OK")
	}
	h.j.reset()
	h.clock.advance(time.Minute)
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)
}

func TestRetry_NewCommitForServiceRetriesAtOnceAndResets(t *testing.T) {
	h := failedAfterSuccess(t)
	// Build up a longer streak first.
	h.clock.advance(retryBase)
	_, _ = h.cycle(t, false)
	if h.r.retry["a"].failures != 2 {
		t.Fatalf("failures = %d, want 2", h.r.retry["a"].failures)
	}
	h.j.reset()
	h.changes.changed["apps/a"] = true
	if _, err := h.cycle(t, true); err == nil {
		t.Fatal("want deploy error")
	}
	if deployCalls(h) != 1 {
		t.Fatalf("journal = %v, want an immediate retry", h.j.entries)
	}
	if got := h.r.retry["a"]; got.failures != 1 || got.next.Sub(h.clock.now()) != retryBase {
		t.Errorf("retry = %+v, want a fresh 30s backoff", got)
	}
}

func TestRetry_UnrelatedCommitDoesNotShortenBackoff(t *testing.T) {
	h := failedAfterSuccess(t)
	h.clock.advance(5 * time.Second)
	_, err := h.cycle(t, true) // changes["apps/a"] is false
	mustNoErr(t, err)
	assertJournal(t, h)
}

func TestRetry_ManifestPathChangeRetriesAtOnce(t *testing.T) {
	h := failedAfterSuccess(t)
	h.loader.cfg = svcs("a:apps/a2:true")
	h.dep.deployFail[aDir] = nil
	h.clock.advance(time.Second)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:/clone/apps/a2")
}

func TestRetry_FullReconcileIgnoresBackoff(t *testing.T) {
	h := failedAfterSuccess(t)
	h.clock.advance(time.Hour)
	if _, err := h.cycle(t, false); err == nil {
		t.Fatal("want deploy error")
	}
	if deployCalls(h) != 1 {
		t.Fatalf("journal = %v, want a deploy", h.j.entries)
	}
	if got := h.r.retry["a"].failures; got != 2 {
		t.Errorf("failures = %d, want 2 (counter keeps growing)", got)
	}
}

func TestRetry_DecryptFailureSharesBackoff(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.dec.failFor["a"] = errDecrypt
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.clock.advance(10 * time.Second)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)

	h.clock.advance(21 * time.Second)
	if _, err := h.cycle(t, false); err == nil {
		t.Fatal("want decrypt error")
	}
	assertJournal(t, h, "decrypt:a") // no deploy while decrypt fails
	if got := h.r.retry["a"].failures; got != 2 {
		t.Errorf("failures = %d, want 2", got)
	}
}

func TestRetry_NeverSucceededServiceBacksOffToo(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.dep.deployFail[aDir] = errDeploy
	_, _ = h.cycle(t, false)

	h.j.reset()
	h.clock.advance(10 * time.Second)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h)

	h.clock.advance(21 * time.Second)
	_, _ = h.cycle(t, false)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
}

func TestRetry_TeardownClearsState(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	h.dep.deployFail[aDir] = errDeploy
	h.changes.changed["apps/a"] = true
	_, _ = h.cycle(t, true)
	if _, ok := h.r.retry["a"]; !ok {
		t.Fatal("no retry state after failure")
	}

	h.loader.cfg = svcs("a:apps/a:false")
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	if _, ok := h.r.retry["a"]; ok {
		t.Error("retry state kept after teardown")
	}

	h.dep.deployFail[aDir] = nil
	h.loader.cfg = svcs("a:apps/a:true")
	h.j.reset()
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
}

func TestRetry_PathChangedErrorFailsOpen(t *testing.T) {
	h := failedAfterSuccess(t)
	h.changes.err = errChange
	h.dep.deployFail[aDir] = nil
	h.clock.advance(time.Second)
	_, err := h.cycle(t, true)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
}

func TestRetry_RestartDeploysEverything(t *testing.T) {
	h := failedAfterSuccess(t)
	h.dep.deployFail[aDir] = nil
	fresh := *h.r
	fresh.deployedOnce, fresh.retry, fresh.deployed, fresh.stage = nil, nil, nil, nil
	fresh.lastFullReconcile = time.Time{}
	h.r = &fresh
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
}

func TestRetry_BackoffNotCountedAsAttemptAndSummarised(t *testing.T) {
	h := failedAfterSuccess(t)
	before := svcStatus(t, h.r.Tracker, "a").LastAttempt
	h.clock.advance(5 * time.Second)
	h.logs.Reset()
	_, _ = h.cycle(t, false)
	if !svcStatus(t, h.r.Tracker, "a").LastAttempt.Equal(before) {
		t.Error("backoff skip recorded as an attempt")
	}
	if got := h.logs.String(); !strings.Contains(got, "backing_off=1") {
		t.Errorf("summary missing backing_off:\n%s", got)
	}
}

// A service that never deployed is not in r.deployed, so teardown does not
// touch it; removing it from the manifest must still drop its backoff.
func TestRetry_DisabledNeverDeployedServiceIsPruned(t *testing.T) {
	h := newHarness(t, "a:apps/a:true")
	h.dep.deployFail[aDir] = errDeploy
	_, _ = h.cycle(t, false)
	if _, ok := h.r.retry["a"]; !ok {
		t.Fatal("no retry state after failure")
	}

	h.loader.cfg = svcs("a:apps/a:false")
	h.clock.advance(time.Second)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	if _, ok := h.r.retry["a"]; ok {
		t.Error("retry state kept for a disabled service")
	}
	if _, ok := h.r.stage["a"]; ok {
		t.Error("stage kept for a disabled service")
	}

	h.dep.deployFail[aDir] = nil
	h.loader.cfg = svcs("a:apps/a:true")
	h.clock.advance(time.Second) // well inside the old backoff
	h.j.reset()
	_, err = h.cycle(t, false)
	mustNoErr(t, err)
	assertJournal(t, h, "decrypt:a", "deploy:"+aDir)
}

func TestRetry_FullReconcileWithPathChangeResetsFailures(t *testing.T) {
	h := failedAfterSuccess(t)
	h.clock.advance(retryBase)
	_, _ = h.cycle(t, false) // failures = 2
	h.loader.cfg = svcs("a:apps/a2:true")
	h.dep.deployFail["/clone/apps/a2"] = errDeploy
	h.clock.advance(time.Hour)
	_, _ = h.cycle(t, false) // full reconcile
	if got := h.r.retry["a"]; got.failures != 1 || got.path != "apps/a2" {
		t.Errorf("retry = %+v, want failures=1 on the new path", got)
	}
}

// Retries must not turn into extra mail: the detector sees "failing" on every
// cycle and "healthy" once, so it yields one failing and one recovered event.
func TestRetry_NotifyOneFailingOneRecovered(t *testing.T) {
	h := failedAfterSuccess(t)
	det := notify.NewDetector(0, h.clock.now)
	key := notify.Key{Kind: notify.KindService, Name: "a"}
	var events []notify.Event
	step := func() {
		ev, _ := det.Step(h.r.observation(h.r.Tracker.Snapshot()).States)
		events = append(events, ev...)
	}
	step()
	for i := 0; i < 3; i++ {
		h.clock.advance(10 * time.Minute) // past any backoff, inside the full interval
		if _, err := h.cycle(t, false); err == nil {
			t.Fatal("want deploy error")
		}
		step()
	}
	// A skipped cycle changes nothing in the observation.
	h.clock.advance(time.Second)
	_, _ = h.cycle(t, false)
	if s := findState(t, h.r.observation(h.r.Tracker.Snapshot()), key); !s.Failing || s.Stage != "deploy" {
		t.Fatalf("state while backed off = %+v", s)
	}
	step()

	h.dep.deployFail[aDir] = nil
	h.clock.advance(10 * time.Minute)
	_, err := h.cycle(t, false)
	mustNoErr(t, err)
	step()

	if len(events) != 2 || events[0].Recovered || !events[1].Recovered {
		t.Errorf("events = %+v, want one failing then one recovered", events)
	}
}
