package schedule

import (
	"testing"
	"time"
)

// newTestScheduler returns a Scheduler driven by a controllable clock and
// with jitter disabled, so delays are exact.
func newTestScheduler(t *testing.T) (*Scheduler, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := New(5*time.Minute, 15*time.Second, 15*time.Minute)
	s.now = func() time.Time { return now }
	s.jitter = func(d time.Duration) time.Duration { return d }
	return s, func(d time.Duration) { now = now.Add(d) }
}

func TestNext_IdleWhenNothingHasChanged(t *testing.T) {
	s, _ := newTestScheduler(t)

	s.Observe(false)
	got, _ := s.Next()

	if got != 5*time.Minute {
		t.Fatalf("delay = %s, want the idle interval 5m", got)
	}
	if s.IsActive() {
		t.Fatal("IsActive = true with no change observed, want false")
	}
}

func TestNext_FastAfterAChange(t *testing.T) {
	s, _ := newTestScheduler(t)

	s.Observe(true)
	got, transitioned := s.Next()

	if got != 15*time.Second {
		t.Fatalf("delay = %s, want the active interval 15s", got)
	}
	if !transitioned {
		t.Fatal("transitioned = false on entering the active window, want true")
	}
	if !s.IsActive() {
		t.Fatal("IsActive = false just after a change, want true")
	}
}

func TestNext_StaysFastWithinTheWindow(t *testing.T) {
	s, advance := newTestScheduler(t)

	s.Observe(true)
	s.Next()

	// Several quiet cycles inside the window: still fast, and no further
	// transitions to log.
	for i := range 3 {
		advance(1 * time.Minute)
		s.Observe(false)
		got, transitioned := s.Next()
		if got != 15*time.Second {
			t.Fatalf("cycle %d: delay = %s, want 15s while inside the window", i, got)
		}
		if transitioned {
			t.Fatalf("cycle %d: transitioned = true, want false while staying active", i)
		}
	}
}

func TestNext_DecaysToIdleAfterTheWindowLapses(t *testing.T) {
	s, advance := newTestScheduler(t)

	s.Observe(true)
	s.Next()

	advance(16 * time.Minute) // past the 15m window
	s.Observe(false)
	got, transitioned := s.Next()

	if got != 5*time.Minute {
		t.Fatalf("delay = %s, want the idle interval 5m after the window lapsed", got)
	}
	if !transitioned {
		t.Fatal("transitioned = false on leaving the active window, want true")
	}
}

func TestObserve_ChangeExtendsTheWindow(t *testing.T) {
	s, advance := newTestScheduler(t)

	s.Observe(true)
	s.Next()

	// A change 10 minutes in should push the window out from there, not
	// leave it expiring at the original deadline.
	advance(10 * time.Minute)
	s.Observe(true)
	s.Next()

	advance(10 * time.Minute) // 20m from the first change, 10m from the second
	s.Observe(false)
	got, _ := s.Next()

	if got != 15*time.Second {
		t.Fatalf("delay = %s, want 15s -- the second change should have extended the window", got)
	}
}

func TestDefaultJitter_StaysWithinTenPercent(t *testing.T) {
	base := 5 * time.Minute
	for range 200 {
		got := defaultJitter(base)
		if got < time.Duration(float64(base)*0.9) || got > time.Duration(float64(base)*1.1) {
			t.Fatalf("jittered delay %s outside ±10%% of %s", got, base)
		}
	}
}
