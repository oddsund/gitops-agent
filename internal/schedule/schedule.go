// Package schedule decides how long the reconcile loop waits between
// cycles: fast for a while after a commit lands, lazy once things go quiet.
package schedule

import (
	"math/rand"
	"time"
)

// Scheduler picks the delay before the next reconcile cycle.
//
// The idea is that commits arrive in bursts -- you push a change, notice
// something's off, push a fix. Polling every 5 minutes makes that second
// push cost 5 minutes of waiting. So: when a cycle finds new commits, drop
// to Active cadence and stay there for ActiveWindow past the last change.
// When the window lapses, decay back to Idle.
//
// The zero value is not usable; construct with New.
type Scheduler struct {
	Idle         time.Duration
	Active       time.Duration
	ActiveWindow time.Duration

	// now and jitter are injected so tests don't have to sleep or cope
	// with randomness.
	now    func() time.Time
	jitter func(time.Duration) time.Duration

	activeUntil time.Time
	wasActive   bool
}

func New(idle, active, activeWindow time.Duration) *Scheduler {
	return &Scheduler{
		Idle:         idle,
		Active:       active,
		ActiveWindow: activeWindow,
		now:          time.Now,
		jitter:       defaultJitter,
	}
}

// defaultJitter spreads the idle poll by up to ±10%, so the agent doesn't
// hit GitHub on a metronome. Not applied to the active cadence, where the
// whole point is predictable responsiveness.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.1
	return d + time.Duration((rand.Float64()*2-1)*spread)
}

// Observe records the outcome of a cycle. Pass changed=true when the sync
// brought in new commits; that (re)opens the active window.
func (s *Scheduler) Observe(changed bool) {
	if changed {
		s.activeUntil = s.now().Add(s.ActiveWindow)
	}
}

// Next returns how long to wait before the next cycle, and whether the
// cadence changed since the last call. Callers should log only on a
// transition -- a line per tick at 15s is unreadable in journalctl.
func (s *Scheduler) Next() (delay time.Duration, transitioned bool) {
	active := s.now().Before(s.activeUntil)
	transitioned = active != s.wasActive
	s.wasActive = active

	if active {
		return s.Active, transitioned
	}
	return s.jitter(s.Idle), transitioned
}

// Active reports whether the scheduler is currently in its fast window,
// for logging and status reporting.
func (s *Scheduler) IsActive() bool { return s.now().Before(s.activeUntil) }
