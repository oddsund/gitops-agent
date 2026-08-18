// Package statusserver tracks the reconcile loop's state in memory and
// serves it over HTTP (/healthz, /, /status.json), so a failed deploy shows
// up on a page you can pull up over the tailnet instead of only as a line
// in journald. See README.md for how it's wired into the loop
// and how a reverse proxy reaches it.
package statusserver

import (
	"sort"
	"sync"
	"time"
)

// ServiceStatus is one service's status as of the most recent cycle that
// touched it. Path and Enabled reflect the last time the service was seen
// in services.toml, which may be older than the deploy fields if the
// service hasn't needed a redeploy in a while.
type ServiceStatus struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`

	LastAttempt time.Time `json:"last_attempt,omitzero"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	// LastAttemptOK is redundant with LastError given the JSON is only
	// produced by this package, but it saves a template needing to know
	// that "no error" and "never attempted" both stringify to "".
	LastAttemptOK bool `json:"last_attempt_ok"`
}

// Status is a full snapshot of the agent's state, served as JSON at
// /status.json and rendered as HTML at /.
type Status struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`

	LastSyncAttempt time.Time `json:"last_sync_attempt,omitzero"`
	LastSyncSuccess time.Time `json:"last_sync_success,omitzero"`
	CommitHash      string    `json:"commit_hash,omitempty"`
	LastSyncError   string    `json:"last_sync_error,omitempty"`

	Services []ServiceStatus `json:"services"`

	NextCycleAt time.Time `json:"next_cycle_at,omitzero"`
	Active      bool      `json:"active"`

	LastCycleAt    time.Time `json:"last_cycle_at,omitzero"`
	LastCycleError string    `json:"last_cycle_error,omitempty"`
}

// Healthy reports whether the last completed cycle finished without errors.
// A Status with no completed cycle yet (LastCycleAt zero) counts as
// healthy -- the agent hasn't failed, it just hasn't run one yet.
func (s Status) Healthy() bool {
	return s.LastCycleError == ""
}

// Tracker is the agent's in-memory status, safe for concurrent use: the
// reconcile loop writes to it from its own goroutine while the HTTP server
// reads it from request goroutines.
type Tracker struct {
	mu       sync.Mutex
	status   Status
	services map[string]ServiceStatus
}

// NewTracker creates a Tracker for an agent that started at startedAt,
// running the given version.
func NewTracker(version string, startedAt time.Time) *Tracker {
	return &Tracker{
		status: Status{
			Version:   version,
			StartedAt: startedAt,
		},
		services: make(map[string]ServiceStatus),
	}
}

// SyncAttempt records that a git sync is starting.
func (t *Tracker) SyncAttempt() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.LastSyncAttempt = time.Now()
}

// SyncResult records the outcome of a git sync. On success commitHash is
// the new HEAD; on failure the previous CommitHash is left untouched, since
// the sync didn't move it.
func (t *Tracker) SyncResult(commitHash string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.status.LastSyncError = err.Error()
		return
	}
	t.status.LastSyncSuccess = time.Now()
	t.status.CommitHash = commitHash
	t.status.LastSyncError = ""
}

// SeenService records a service's current path/enabled state from the
// services manifest, independent of whether it was actually deployed this
// cycle -- so a disabled or up-to-date service still shows up on the page.
func (t *Tracker) SeenService(name, path string, enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	svc := t.services[name]
	svc.Name = name
	svc.Path = path
	svc.Enabled = enabled
	t.services[name] = svc
}

// ServiceAttempt records that a deploy or teardown is starting for a
// service.
func (t *Tracker) ServiceAttempt(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	svc := t.services[name]
	svc.Name = name
	svc.LastAttempt = time.Now()
	t.services[name] = svc
}

// ServiceResult records the outcome of a deploy or teardown for a service.
func (t *Tracker) ServiceResult(name string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	svc := t.services[name]
	svc.Name = name
	if err != nil {
		svc.LastAttemptOK = false
		svc.LastError = err.Error()
	} else {
		svc.LastAttemptOK = true
		svc.LastError = ""
		svc.LastSuccess = time.Now()
	}
	t.services[name] = svc
}

// SetNextCycle records when the loop expects to run next, and whether it's
// currently in the post-commit active window.
func (t *Tracker) SetNextCycle(at time.Time, active bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.NextCycleAt = at
	t.status.Active = active
}

// CycleComplete records that a full reconcile cycle finished, with err
// being the joined error from every service that failed this cycle (nil if
// none did). This -- not the sync error alone -- is what /healthz reports.
func (t *Tracker) CycleComplete(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.LastCycleAt = time.Now()
	if err != nil {
		t.status.LastCycleError = err.Error()
	} else {
		t.status.LastCycleError = ""
	}
}

// Snapshot returns a copy of the current status, safe to read without
// holding the tracker's lock. Services are sorted by name for a stable
// render.
func (t *Tracker) Snapshot() Status {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.status
	s.Services = make([]ServiceStatus, 0, len(t.services))
	for _, svc := range t.services {
		s.Services = append(s.Services, svc)
	}
	sort.Slice(s.Services, func(i, j int) bool { return s.Services[i].Name < s.Services[j].Name })
	return s
}
