// Package reconcile is the agent's sync -> decrypt -> deploy loop. Every
// side effect on the outside world (git, sops, docker compose, the services
// manifest) goes through a small interface defined here, so the loop's
// decisions can be tested without a git remote, sops keys or docker.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/gitsync"
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

	// State carried across cycles: the last-known-good services manifest
	// (so a broken commit doesn't stop deploys), the set of services
	// deployed at least once since startup, and when the last full
	// reconcile ran.
	svcCfg            *config.ServicesConfig
	deployedOnce      map[string]bool
	deployed          state.State // name -> serviceDir, persisted; see internal/state
	lastFullReconcile time.Time   // zero => first cycle is a full one
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
	r.deployed = state.Load(r.Config.StatePath)
}

// Run is the reconcile loop. It polls at Config.Idle when idle, drops to
// the active cadence for a while after a commit lands, and returns when ctx
// is cancelled. reload is a "reconcile now" nudge (SIGHUP) and may be nil.
func (r *Reconciler) Run(ctx context.Context, reload <-chan struct{}) error {
	idle := r.Config.Idle
	active := r.Config.Active
	window := r.Config.ActiveWindow
	fullReconcileEvery := r.Config.FullReconcileEvery

	log.Printf("polling %s (branch %s): every %s when idle, %s for %s after a change; full reconcile every %s",
		r.Config.RepoURL, r.Config.Branch, idle, active, window, fullReconcileEvery)

	sched := schedule.New(idle, active, window)

	r.start()

	for {
		changed, err := r.RunOnce(ctx)
		if err != nil {
			log.Printf("run failed, will try again next cycle: %v", err)
		}
		r.Tracker.CycleComplete(err)

		sched.Observe(changed)
		delay, transitioned := sched.Next()
		if transitioned {
			if sched.IsActive() {
				log.Printf("new commits: reconciling every %s for the next %s", active, window)
			} else {
				log.Printf("quiet for %s, back to polling every %s", window, idle)
			}
		}
		r.Tracker.SetNextCycle(r.now().Add(delay), sched.IsActive())

		if r.StatusFile != "" {
			if err := statusserver.WriteFile(r.StatusFile, r.Tracker.Snapshot()); err != nil {
				log.Printf("status: writing %s: %v", r.StatusFile, err)
			}
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

	r.Tracker.SyncAttempt()
	res, err := r.Syncer.Sync()
	if err != nil {
		r.Tracker.SyncResult("", err)
		return false, fmt.Errorf("syncing %s: %w", r.Config.RepoURL, err)
	}
	r.Tracker.SyncResult(res.After.String(), nil)

	manifestPath := filepath.Join(r.Config.ClonePath, servicesManifest)
	if loaded, err := r.Services.LoadServices(manifestPath); err != nil {
		log.Printf("services manifest %s is broken, keeping last-known-good list: %v", manifestPath, err)
	} else {
		r.svcCfg = loaded
	}

	if r.svcCfg == nil {
		log.Printf("no valid services manifest loaded yet -- nothing to deploy this cycle")
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
		log.Printf("periodic full reconcile: deploying every enabled service regardless of changes")
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
		log.Printf("service %s: no longer enabled, tearing down (%s)", name, serviceDir)
		r.Tracker.ServiceAttempt(name)
		if err := r.Deployer.Down(serviceDir); err != nil {
			errs = append(errs, fmt.Errorf("tearing down %s: %w", name, err))
			r.Tracker.ServiceResult(name, err)
			continue
		}
		delete(r.deployed, name)
		delete(r.deployedOnce, name) // a later re-enable must force a fresh deploy
		stateDirty = true
		r.Tracker.ServiceResult(name, nil)
		log.Printf("tore down %s (%s)", name, serviceDir)
		// Only after a successful Down: until then the containers may still
		// need the env file. A removal failure does not undo the teardown.
		if err := r.Decrypter.RemoveServiceSecrets(name); err != nil {
			log.Printf("service %s: removing decrypted secrets: %v", name, err)
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
			log.Printf("service %s: removing decrypted secrets: %v", svc.Name, err)
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

		log.Printf("service %s: decrypting secrets", svc.Name)
		if err := r.Decrypter.DecryptServiceSecrets(serviceDir, svc.Name); err != nil {
			errs = append(errs, fmt.Errorf("decrypting secrets for %s: %w", svc.Name, err))
			r.Tracker.ServiceResult(svc.Name, err)
			continue
		}

		log.Printf("service %s: running docker compose up", svc.Name)
		if err := r.Deployer.Deploy(serviceDir); err != nil {
			errs = append(errs, fmt.Errorf("deploying %s: %w", svc.Name, err))
			r.Tracker.ServiceResult(svc.Name, err)
			continue
		}
		r.Tracker.ServiceResult(svc.Name, nil)

		r.deployedOnce[svc.Name] = true
		if r.deployed[svc.Name] != serviceDir {
			r.deployed[svc.Name] = serviceDir
			stateDirty = true
		}
		deployedCount++
		log.Printf("deployed %s (%s)", svc.Name, serviceDir)
	}

	if stateDirty {
		if err := state.Save(r.Config.StatePath, r.deployed); err != nil {
			errs = append(errs, fmt.Errorf("saving deploy state %s: %w", r.Config.StatePath, err))
		}
	}

	// One summary line, not one per service: at the active cadence this
	// runs every 15s and would otherwise bury everything else in the
	// journal.
	if skipped > 0 {
		log.Printf("cycle complete at %s: %d deployed, %d already up to date", res.After.String()[:12], deployedCount, skipped)
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
		log.Printf("service %s: couldn't tell whether %s changed, deploying to be safe: %v", svc.Name, svc.Path, err)
		return true
	}
	return changed
}
