// Package reconcile is the agent's sync -> decrypt -> deploy loop. Every
// side effect on the outside world (git, sops, docker compose, the services
// manifest) goes through a small interface defined here, so the loop's
// decisions can be tested without a git remote, sops keys or docker.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/notify"
	"github.com/oddsund/gitops-agent/internal/schedule"
	"github.com/oddsund/gitops-agent/internal/state"
	"github.com/oddsund/gitops-agent/internal/statusserver"
)

// servicesManifest is the services list's path relative to the repo root.
// It's tracked in git, unlike config.toml, so enabling a service is a
// commit, not an SSH session.
const servicesManifest = "services.toml"

// Syncer brings the local clone up to date. The adapter binds repo URL,
// branch, clone path and auth.
type Syncer interface {
	Sync() (gitsync.Result, error)
}

// ChangeDetector reports whether path (relative to the repo root) differs
// between res.Before and res.After. The adapter binds the clone path.
type ChangeDetector interface {
	PathChanged(res gitsync.Result, path string) (bool, error)
}

// ServiceLoader reads and validates the services manifest at path.
type ServiceLoader interface {
	LoadServices(path string) (*config.ServicesConfig, error)
}

// Decrypter writes and removes a service's decrypted secrets. The adapter
// binds the SSH key path and secrets base dir.
type Decrypter interface {
	// DecryptServiceSecrets writes the decrypted file. When serviceDir has
	// no secrets.enc.env it removes any stale decrypted file instead.
	DecryptServiceSecrets(serviceDir, serviceName string) error
	// RemoveServiceSecrets deletes what DecryptServiceSecrets wrote for
	// serviceName. A file that is already gone is success.
	RemoveServiceSecrets(serviceName string) error
}

// Deployer runs docker compose in a service directory.
type Deployer interface {
	Deploy(serviceDir string) error
	Down(serviceDir string) error
}

// Notifier is told the outcome of every cycle. Implementations must not
// block on I/O and must not fail the caller. Nil means notifications are off.
type Notifier interface{ Observe(notify.Observation) }

// Config is the subset of the agent config the loop needs. Plain values so
// tests don't build an AgentConfig.
type Config struct {
	RepoURL            string // used only in the sync error message and startup log
	Branch             string // used only in the startup log
	ClonePath          string
	StatePath          string
	Idle               time.Duration // cfg.Git.PullIntervalSeconds
	Active             time.Duration // cfg.Git.ActiveIntervalSeconds
	ActiveWindow       time.Duration // cfg.Git.ActiveWindowSeconds
	FullReconcileEvery time.Duration // cfg.Git.FullReconcileIntervalSeconds
}

// Reconciler runs the reconcile loop. Fill in the exported fields with a
// struct literal; the zero value of the unexported loop state is ready to
// use.
type Reconciler struct {
	Config    Config
	Syncer    Syncer
	Changes   ChangeDetector
	Services  ServiceLoader
	Decrypter Decrypter
	Deployer  Deployer
	Tracker   *statusserver.Tracker
	// StatusFile is where Run writes the status snapshot after each cycle.
	// Empty disables the write (tests).
	StatusFile string
	// Now defaults to time.Now. Only the full-reconcile timer and
	// SetNextCycle read it.
	Now func() time.Time
	// Log receives the reconciler's log lines. Nil discards.
	Log *slog.Logger
	// Notifier hears about each finished cycle from Run (not RunOnce). Nil
	// disables. Assign it only when non-nil: a typed nil pointer in this
	// interface would not compare equal to nil.
	Notifier Notifier

	// State carried across cycles: the last-known-good services manifest
	// (so a broken commit doesn't stop deploys), the set of services
	// deployed at least once since startup, and when the last full
	// reconcile ran.
	svcCfg            *config.ServicesConfig
	deployedOnce      map[string]bool
	deployed          state.State       // name -> serviceDir, persisted; see internal/state
	lastFullReconcile time.Time         // zero => first cycle is a full one
	cycle             int               // RunOnce calls so far; 1 on the first
	lg                *slog.Logger      // r.log() tagged with the current cycle
	stage             map[string]string // service -> stage of its current failure; see observation
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// start initialises the loop state on first use. It is not done in a
// constructor so that a state.Load warning is logged after the "polling"
// line in Run, as it always was.
func (r *Reconciler) start() {
	if r.deployed != nil {
		return
	}
	r.deployedOnce = make(map[string]bool)
	r.stage = make(map[string]string)
	r.deployed = state.Load(r.Config.StatePath, r.log().With("component", "state"))
}

// Run is the reconcile loop. It polls at Config.Idle when idle, drops to
// the active cadence for a while after a commit lands, and returns when ctx
// is cancelled. reload is a "reconcile now" nudge (SIGHUP) and may be nil.
func (r *Reconciler) Run(ctx context.Context, reload <-chan struct{}) error {
	idle := r.Config.Idle
	active := r.Config.Active
	window := r.Config.ActiveWindow
	fullReconcileEvery := r.Config.FullReconcileEvery

	r.log().Info("polling for changes", "repo", gitsync.RedactURL(r.Config.RepoURL), "branch", r.Config.Branch,
		"idle", idle, "active", active, "active_window", window, "full_reconcile_every", fullReconcileEvery)

	sched := schedule.New(idle, active, window)

	r.start()

	for {
		changed, err := r.RunOnce(ctx)
		if err != nil {
			r.log().Error("run failed, will try again next cycle", "cycle", r.cycle, "error", err)
		}
		r.Tracker.CycleComplete(err)

		sched.Observe(changed)
		delay, transitioned := sched.Next()
		if transitioned {
			if sched.IsActive() {
				r.log().Info("new commits: reconciling more often", "interval", active, "window", window)
			} else {
				r.log().Info("quiet, back to the idle cadence", "quiet_for", window, "interval", idle)
			}
		}
		r.Tracker.SetNextCycle(r.now().Add(delay), sched.IsActive())

		if r.StatusFile != "" {
			if err := statusserver.WriteFile(r.StatusFile, r.Tracker.Snapshot()); err != nil {
				r.log().Error("writing status file", "path", r.StatusFile, "error", err)
			}
		}

		// After the cycle result and the status file are final, so a
		// notification problem cannot influence either.
		if r.Notifier != nil {
			r.notify()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-reload:
			// SIGHUP: skip the remaining wait.
		case <-time.After(delay):
		}
	}
}

// RunOnce syncs the config repo, reloads the services manifest, then
// decrypts and deploys every enabled service that needs it; one service's
// failure doesn't stop the others. It reports whether the sync brought in
// new commits, which drives the polling cadence. ctx is accepted for
// callers that will need it but is deliberately not consulted: honouring it
// would change behaviour.
func (r *Reconciler) RunOnce(ctx context.Context) (bool, error) {
	r.start()
	r.cycle++
	lg := r.log().With("cycle", r.cycle)
	r.lg = lg

	r.Tracker.SyncAttempt()
	res, err := r.Syncer.Sync()
	if err != nil {
		r.Tracker.SyncResult("", err)
		return false, fmt.Errorf("syncing %s: %w", gitsync.RedactURL(r.Config.RepoURL), err)
	}
	r.Tracker.SyncResult(res.After.String(), nil)

	manifestPath := filepath.Join(r.Config.ClonePath, servicesManifest)
	if loaded, err := r.Services.LoadServices(manifestPath); err != nil {
		lg.Warn("services manifest is broken, keeping last-known-good list", "path", manifestPath, "error", err)
	} else {
		r.svcCfg = loaded
		lg.Debug("services manifest loaded", "path", manifestPath, "services", len(loaded.Services), "enabled", len(loaded.EnabledServices()))
	}

	if r.svcCfg == nil {
		lg.Warn("no valid services manifest loaded yet -- nothing to deploy this cycle")
		return res.Changed, nil
	}
	for _, svc := range r.svcCfg.Services {
		r.Tracker.SeenService(svc.Name, svc.Path, svc.Enabled)
	}

	// Drift (a container stopped by hand, one that died for good) isn't
	// visible in git, so it would never be picked up by change detection
	// alone. A periodic unconditional pass is the cheap way to heal it.
	fullReconcile := r.now().Sub(r.lastFullReconcile) >= r.Config.FullReconcileEvery
	if fullReconcile {
		lg.Info("periodic full reconcile: deploying every enabled service regardless of changes")
		r.lastFullReconcile = r.now()
	}

	var errs []error
	stateDirty := false

	// Tear down before deploying: a service renamed in the same commit
	// (removed under one name, added under another, same ports) must free
	// its port before the new name tries to bind it. r.deployed only ever
	// holds services this agent has actually deployed, so this only touches
	// docker for a service that *was* enabled and just stopped being so --
	// nothing to gate on fullReconcile here, it's already cheap.
	desired := make(map[string]bool, len(r.svcCfg.Services))
	for _, svc := range r.svcCfg.EnabledServices() {
		desired[svc.Name] = true
	}
	tornDown := map[string]bool{}
	for name, serviceDir := range r.deployed {
		if desired[name] {
			continue
		}
		svcLog := lg.With("service", name)
		svcLog.Info("no longer enabled, tearing down", "path", serviceDir)
		r.Tracker.ServiceAttempt(name)
		if err := r.Deployer.Down(serviceDir); err != nil {
			svcLog.Error("tearing down failed", "path", serviceDir, "error", err)
			errs = append(errs, fmt.Errorf("tearing down %s: %w", name, err))
			r.Tracker.ServiceResult(name, err)
			r.stage[name] = "teardown"
			continue
		}
		delete(r.deployed, name)
		delete(r.stage, name)
		delete(r.deployedOnce, name) // a later re-enable must force a fresh deploy
		stateDirty = true
		r.Tracker.ServiceResult(name, nil)
		svcLog.Info("tore down", "path", serviceDir)
		// Only after a successful Down: until then the containers may still
		// need the env file. A removal failure does not undo the teardown.
		if err := r.Decrypter.RemoveServiceSecrets(name); err != nil {
			svcLog.Error("removing decrypted secrets failed", "error", err)
			errs = append(errs, fmt.Errorf("removing secrets for %s: %w", name, err))
		}
		tornDown[name] = true
	}

	// Sweep services that are disabled in the manifest: a file left by an
	// earlier cycle or a failed removal is retried every cycle. Names come
	// only from the manifest, never from scanning the secrets directory.
	for _, svc := range r.svcCfg.Services {
		if svc.Enabled || tornDown[svc.Name] {
			continue
		}
		// Still deployed means Down failed: the containers may still be
		// running and need their env file on restart.
		if _, stillDeployed := r.deployed[svc.Name]; stillDeployed {
			continue
		}
		if err := r.Decrypter.RemoveServiceSecrets(svc.Name); err != nil {
			lg.With("service", svc.Name).Error("removing decrypted secrets failed", "error", err)
			errs = append(errs, fmt.Errorf("removing secrets for %s: %w", svc.Name, err))
		}
	}

	var deployedCount, skipped int
	for _, svc := range r.svcCfg.EnabledServices() {
		serviceDir := filepath.Join(r.Config.ClonePath, svc.Path)

		if !fullReconcile && r.deployedOnce[svc.Name] && !r.serviceNeedsDeploy(res, svc) {
			skipped++
			continue
		}

		r.Tracker.ServiceAttempt(svc.Name)
		start := r.now()
		svcLog := lg.With("service", svc.Name)

		svcLog.Info("decrypting secrets")
		if err := r.Decrypter.DecryptServiceSecrets(serviceDir, svc.Name); err != nil {
			svcLog.Error("decrypting secrets failed", "error", err)
			errs = append(errs, fmt.Errorf("decrypting secrets for %s: %w", svc.Name, err))
			r.Tracker.ServiceResult(svc.Name, err)
			r.stage[svc.Name] = "decrypt"
			// Without this a service deployed earlier is skipped as up to
			// date next cycle, and a failed stale-secret removal is never retried.
			delete(r.deployedOnce, svc.Name)
			continue
		}

		svcLog.Info("running docker compose up")
		if err := r.Deployer.Deploy(serviceDir); err != nil {
			svcLog.Error("deploy failed", "path", serviceDir, "duration", r.now().Sub(start), "error", err)
			errs = append(errs, fmt.Errorf("deploying %s: %w", svc.Name, err))
			r.Tracker.ServiceResult(svc.Name, err)
			r.stage[svc.Name] = "deploy"
			continue
		}
		r.Tracker.ServiceResult(svc.Name, nil)
		delete(r.stage, svc.Name)

		r.deployedOnce[svc.Name] = true
		if r.deployed[svc.Name] != serviceDir {
			r.deployed[svc.Name] = serviceDir
			stateDirty = true
		}
		deployedCount++
		svcLog.Info("deployed", "path", serviceDir, "duration", r.now().Sub(start))
	}

	if stateDirty {
		if err := state.Save(r.Config.StatePath, r.deployed); err != nil {
			lg.Error("saving deploy state failed", "path", r.Config.StatePath, "error", err)
			errs = append(errs, fmt.Errorf("saving deploy state %s: %w", r.Config.StatePath, err))
		}
	}

	// One summary line, not one per service: at the active cadence this
	// runs every 15s and would otherwise bury everything else in the
	// journal.
	if skipped > 0 {
		lg.Info("cycle complete", "commit", gitsync.Short(res.After), "deployed", deployedCount, "up_to_date", skipped)
	}
	return res.Changed, errors.Join(errs...)
}

// serviceNeedsDeploy reports whether svc's files moved in this sync. It
// fails open: if the comparison can't be made, deploy anyway. A redundant
// deploy is cheap and idempotent; a skipped one leaves the host wrong.
func (r *Reconciler) serviceNeedsDeploy(res gitsync.Result, svc config.Service) bool {
	if !res.Changed {
		return false
	}
	changed, err := r.Changes.PathChanged(res, svc.Path)
	if err != nil {
		r.log().With("cycle", r.cycle, "service", svc.Name).Warn("couldn't tell whether the service changed, deploying to be safe", "path", svc.Path, "error", err)
		return true
	}
	return changed
}

// notify hands the finished cycle to the Notifier. It cannot affect the
// cycle: no return value, and a panic is contained here.
func (r *Reconciler) notify() {
	defer func() {
		if p := recover(); p != nil {
			r.log().Error("notifier panicked", "panic", fmt.Sprint(p))
		}
	}()
	r.Notifier.Observe(r.observation(r.Tracker.Snapshot()))
}

// observation turns the tracker snapshot into per-key health. The tracker
// keeps error strings only, so the failing stage comes from r.stage rather
// than from parsing error text. A service that was never attempted is not
// failing and is left out.
func (r *Reconciler) observation(snap statusserver.Status) notify.Observation {
	syncErr := snap.LastSyncError
	if url := r.Config.RepoURL; url != "" {
		// Belt and braces: the error text is already redacted where it is
		// created.
		syncErr = strings.ReplaceAll(syncErr, url, gitsync.RedactURL(url))
	}
	obs := notify.Observation{
		States: []notify.State{{
			Key:     notify.Key{Kind: notify.KindSync},
			Failing: snap.LastSyncError != "",
			Stage:   "sync",
			Err:     syncErr,
		}},
	}
	if len(snap.CommitHash) >= 12 {
		obs.Commit = snap.CommitHash[:12]
	}
	for _, s := range snap.Services {
		if s.LastAttempt.IsZero() {
			continue
		}
		obs.States = append(obs.States, notify.State{
			Key:     notify.Key{Kind: notify.KindService, Name: s.Name},
			Failing: !s.LastAttemptOK,
			Stage:   r.stage[s.Name],
			Err:     s.LastError,
		})
	}
	return obs
}
