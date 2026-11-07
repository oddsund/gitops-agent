package notify

import "time"

// Event is one transition worth telling the operator about.
type Event struct {
	Key       Key
	Recovered bool
	Stage     string
	Err       string
}

// Detector turns per-cycle health into transitions, with a per-key rate
// limit on failure mail. It is not safe for concurrent use; Service
// serialises access.
type Detector struct {
	min  time.Duration
	now  func() time.Time
	keys map[Key]*keyState
}

type keyState struct {
	failSent     bool // a "failing" mail was emitted and no "recovered" yet
	lastFailSent time.Time
}

// NewDetector returns a Detector that waits at least min between two
// failing mails for the same key.
func NewDetector(min time.Duration, now func() time.Time) *Detector {
	return &Detector{min: min, now: now, keys: make(map[Key]*keyState)}
}

// Step returns the events for one cycle and how many failures it held back.
//
// A rate-limited failure is held, not dropped: the key stays "failing but
// unreported", so the first cycle after the interval emits it. A detector
// that looked only at transitions would lose it for good. Recovery is never
// rate limited, and is sent only for a failure that was actually mailed.
func (d *Detector) Step(states []State) (events []Event, held int) {
	now := d.now()
	for _, s := range states {
		ks := d.keys[s.Key]
		if ks == nil {
			ks = &keyState{}
			d.keys[s.Key] = ks
		}
		switch {
		case s.Failing && ks.failSent:
			// Still broken and already reported: this is what keeps a 15s
			// cadence from producing a mail per cycle.
		case s.Failing:
			if ks.lastFailSent.IsZero() || now.Sub(ks.lastFailSent) >= d.min {
				events = append(events, Event{Key: s.Key, Stage: s.Stage, Err: s.Err})
				ks.failSent = true
				ks.lastFailSent = now
			} else {
				held++
			}
		case ks.failSent:
			events = append(events, Event{Key: s.Key, Recovered: true})
			ks.failSent = false
		}
	}
	return events, held
}
