package notify

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func svc(name string, failing bool) State {
	return State{Key: Key{KindService, name}, Failing: failing, Stage: "deploy"}
}

func step(t *testing.T, d *Detector, wantEvents, wantHeld int, states ...State) []Event {
	t.Helper()
	ev, held := d.Step(states)
	if len(ev) != wantEvents || held != wantHeld {
		t.Fatalf("events=%d held=%d, want events=%d held=%d (%+v)", len(ev), held, wantEvents, wantHeld, ev)
	}
	return ev
}

func TestDetector_InitialHealthyEmitsNothing(t *testing.T) {
	c := &clock{t0}
	d := NewDetector(time.Hour, c.now)
	for i := 0; i < 3; i++ {
		step(t, d, 0, 0, svc("a", false), State{Key: Key{Kind: KindSync}})
	}
}

func TestDetector_FailingOnceThenSilent(t *testing.T) {
	c := &clock{t0}
	d := NewDetector(time.Hour, c.now)
	ev := step(t, d, 1, 0, svc("a", true))
	if ev[0].Recovered || ev[0].Stage != "deploy" {
		t.Errorf("event = %+v", ev[0])
	}
	for i := 0; i < 10; i++ {
		c.advance(15 * time.Second)
		step(t, d, 0, 0, svc("a", true))
	}
}

func TestDetector_RecoveryOnce(t *testing.T) {
	c := &clock{t0}
	d := NewDetector(time.Hour, c.now)
	step(t, d, 1, 0, svc("a", true))
	ev := step(t, d, 1, 0, svc("a", false))
	if !ev[0].Recovered {
		t.Errorf("event = %+v", ev[0])
	}
	step(t, d, 0, 0, svc("a", false))
	step(t, d, 0, 0, svc("a", false))
}

func TestDetector_FailureAlreadyPresentAtFirstObservation(t *testing.T) {
	d := NewDetector(time.Hour, (&clock{t0}).now)
	step(t, d, 1, 0, svc("a", true))
	step(t, d, 0, 0, svc("a", true))
}

func TestDetector_RateLimitHoldsAndLaterEmits(t *testing.T) {
	c := &clock{t0}
	min := 10 * time.Minute
	d := NewDetector(min, c.now)
	step(t, d, 1, 0, svc("a", true)) // t0, sent
	c.advance(10 * time.Second)
	step(t, d, 1, 0, svc("a", false)) // recovery is never held back
	c.advance(10 * time.Second)
	step(t, d, 0, 1, svc("a", true)) // held
	c.t = t0.Add(min - time.Second)
	step(t, d, 0, 1, svc("a", true)) // still held, counted again
	c.t = t0.Add(min)
	step(t, d, 1, 0, svc("a", true)) // interval elapsed: now it goes out
	step(t, d, 0, 0, svc("a", true))
}

func TestDetector_RecoveryOfHeldFailureIsSilent(t *testing.T) {
	c := &clock{t0}
	d := NewDetector(time.Hour, c.now)
	step(t, d, 1, 0, svc("a", true))
	c.advance(time.Second)
	step(t, d, 1, 0, svc("a", false))
	c.advance(time.Second)
	step(t, d, 0, 1, svc("a", true))
	c.advance(time.Second)
	step(t, d, 0, 0, svc("a", false)) // never told about the failure: no recovery mail
}

func TestDetector_KeysAreIndependent(t *testing.T) {
	d := NewDetector(time.Hour, (&clock{t0}).now)
	sync := State{Key: Key{Kind: KindSync}, Failing: true, Stage: "sync"}
	ev := step(t, d, 2, 0, svc("a", true), svc("b", true))
	if ev[0].Key.Name != "a" || ev[1].Key.Name != "b" {
		t.Errorf("order: %+v", ev)
	}
	// A service called "agent" does not collide with the sync step.
	ev = step(t, d, 1, 0, sync)
	if ev[0].Key.Kind != KindSync {
		t.Errorf("event = %+v", ev[0])
	}
	step(t, d, 1, 0, svc("agent", true))
	ev = step(t, d, 1, 0, svc("a", false))
	if ev[0].Key.Name != "a" || !ev[0].Recovered {
		t.Errorf("event = %+v", ev[0])
	}
}

func TestDetector_MissingKeysKeepTheirState(t *testing.T) {
	c := &clock{t0}
	d := NewDetector(time.Hour, c.now)
	step(t, d, 1, 0, svc("a", true))
	step(t, d, 0, 0, svc("b", false)) // "a" absent this cycle
	step(t, d, 0, 0, svc("a", true))  // still reported, still silent
	ev := step(t, d, 1, 0, svc("a", false))
	if !ev[0].Recovered {
		t.Errorf("event = %+v", ev[0])
	}
}
