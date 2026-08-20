// Command gitops-agent is a long-running process that syncs a configured
// git repo, decrypts each enabled service's secrets, and deploys it via
// docker compose.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/schedule"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
	"github.com/oddsund/gitops-agent/internal/state"
	"github.com/oddsund/gitops-agent/internal/statusserver"
)

// servicesManifest is the services list's path relative to the repo root.
// It's tracked in git, unlike config.toml, so enabling a service is a
// commit, not an SSH session.
const servicesManifest = "services.toml"

// version is set at build time via -ldflags "-X main.version=..." (see
// .github/workflows/release.yml), which passes the release
// tag -- the exact string update.bash also writes to installed-version, so
// a provisioning check can compare the two with a plain string equality
// check. A plain `go build` leaves it at "dev".
var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/gitops-agent/config.toml", "path to config.toml")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log.Printf("gitops-agent starting up, version %s, config: %s", version, *configPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	auth, err := gitsync.SSHAuth(cfg.Sops.SSHKeyPath)
	if err != nil {
		log.Fatalf("loading SSH key: %v", err)
	}

	// SIGTERM/SIGINT cancel the loop so `systemctl restart` doesn't
	// guillotine a docker compose run half way through. SIGHUP is
	// separate: it means "reconcile now", for when you're on the box and
	// don't want to wait out the current sleep.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	reloadCh := make(chan struct{}, 1)
	go watchSIGHUP(ctx, reloadCh)

	tracker := statusserver.NewTracker(version, time.Now())
	startStatusServer(ctx, cfg.Status.ListenAddr, tracker)

	if err := run(ctx, cfg, auth, reloadCh, tracker); err != nil {
		log.Fatalf("gitops-agent: %v", err)
	}
	log.Printf("gitops-agent: shutting down")
}

// startStatusServer serves the status page (internal/statusserver) in the
// background. A failure to bind is logged, not fatal: the reconcile loop is
// the agent's actual job, and losing the status page over it would be a
// worse outage than the one it exists to report on.
func startStatusServer(ctx context.Context, listenAddr string, tracker *statusserver.Tracker) {
	srv := &http.Server{Addr: listenAddr, Handler: tracker.Handler()}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("status server: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("status server: shutdown: %v", err)
		}
	}()
	log.Printf("status page listening on %s", listenAddr)
}

// watchSIGHUP turns every SIGHUP into a "reconcile now" nudge on reloadCh.
// signal.Notify rather than NotifyContext: this has to fire repeatedly, and
// NotifyContext is one-shot.
func watchSIGHUP(ctx context.Context, reloadCh chan<- struct{}) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			log.Printf("received SIGHUP, reconciling now")
			select {
			case reloadCh <- struct{}{}:
			default: // a reconcile is already pending, no need to queue another
			}
		}
	}
}

// run is the reconcile loop. It polls at cfg.Git.PullIntervalSeconds when
// idle, drops to the active cadence for a while after a commit lands, and
// returns when ctx is cancelled.
func run(ctx context.Context, cfg *config.AgentConfig, auth transport.AuthMethod, reloadCh <-chan struct{}, tracker *statusserver.Tracker) error {
	idle := time.Duration(cfg.Git.PullIntervalSeconds) * time.Second
	active := time.Duration(cfg.Git.ActiveIntervalSeconds) * time.Second
	window := time.Duration(cfg.Git.ActiveWindowSeconds) * time.Second
	fullReconcileEvery := time.Duration(cfg.Git.FullReconcileIntervalSeconds) * time.Second

	log.Printf("polling %s (branch %s): every %s when idle, %s for %s after a change; full reconcile every %s",
		cfg.Git.RepoURL, cfg.Git.Branch, idle, active, window, fullReconcileEvery)

	sched := schedule.New(idle, active, window)

	// state carried across cycles: the last-known-good services manifest
	// (so a broken commit doesn't stop deploys), the set of services
	// deployed at least once since startup, and when the last full
	// reconcile ran.
	st := &loopState{
		deployedOnce:      make(map[string]bool),
		deployed:          state.Load(cfg.State.Path),
		lastFullReconcile: time.Time{}, // zero => first cycle is a full one
	}

	for {
		changed, err := runOnce(cfg, st, auth, fullReconcileEvery, tracker)
		if err != nil {
			log.Printf("run failed, will try again next cycle: %v", err)
		}
		tracker.CycleComplete(err)

		sched.Observe(changed)
		delay, transitioned := sched.Next()
		if transitioned {
			if sched.IsActive() {
				log.Printf("new commits: reconciling every %s for the next %s", active, window)
			} else {
				log.Printf("quiet for %s, back to polling every %s", window, idle)
			}
		}
		tracker.SetNextCycle(time.Now().Add(delay), sched.IsActive())

		if err := statusserver.WriteFile(statusserver.DefaultStatusFilePath, tracker.Snapshot()); err != nil {
			log.Printf("status: writing %s: %v", statusserver.DefaultStatusFilePath, err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-reloadCh:
			// SIGHUP: skip the remaining wait.
		case <-time.After(delay):
		}
	}
}

type loopState struct {
	svcCfg            *config.ServicesConfig
	deployedOnce      map[string]bool
	deployed          state.State // name -> serviceDir, persisted; see internal/state
	lastFullReconcile time.Time
}

// runOnce syncs the config repo, reloads the services manifest, then
// decrypts and deploys every enabled service that needs it; one service's
// failure doesn't stop the others. It reports whether the sync brought in
// new commits, which drives the polling cadence.
func runOnce(cfg *config.AgentConfig, st *loopState, auth transport.AuthMethod, fullReconcileEvery time.Duration, tracker *statusserver.Tracker) (bool, error) {
	tracker.SyncAttempt()
	res, err := gitsync.Sync(gitsync.Config{
		RepoURL:   cfg.Git.RepoURL,
		Branch:    cfg.Git.Branch,
		ClonePath: cfg.Git.ClonePath,
	}, auth)
	if err != nil {
		tracker.SyncResult("", err)
		return false, fmt.Errorf("syncing %s: %w", cfg.Git.RepoURL, err)
	}
	tracker.SyncResult(res.After.String(), nil)

	manifestPath := filepath.Join(cfg.Git.ClonePath, servicesManifest)
	if loaded, err := config.LoadServices(manifestPath); err != nil {
		log.Printf("services manifest %s is broken, keeping last-known-good list: %v", manifestPath, err)
	} else {
		st.svcCfg = loaded
	}

	if st.svcCfg == nil {
		log.Printf("no valid services manifest loaded yet -- nothing to deploy this cycle")
		return res.Changed, nil
	}
	for _, svc := range st.svcCfg.Services {
		tracker.SeenService(svc.Name, svc.Path, svc.Enabled)
	}

	// Drift (a container stopped by hand, one that died for good) isn't
	// visible in git, so it would never be picked up by change detection
	// alone. A periodic unconditional pass is the cheap way to heal it.
	fullReconcile := time.Since(st.lastFullReconcile) >= fullReconcileEvery
	if fullReconcile {
		log.Printf("periodic full reconcile: deploying every enabled service regardless of changes")
		st.lastFullReconcile = time.Now()
	}

	var errs []error
	stateDirty := false

	// Tear down before deploying: a service renamed in the same commit
	// (removed under one name, added under another, same ports) must free
	// its port before the new name tries to bind it. st.deployed only ever
	// holds services this agent has actually deployed, so this only touches
	// docker for a service that *was* enabled and just stopped being so --
	// nothing to gate on fullReconcile here, it's already cheap.
	desired := make(map[string]bool, len(st.svcCfg.Services))
	for _, svc := range st.svcCfg.EnabledServices() {
		desired[svc.Name] = true
	}
	for name, serviceDir := range st.deployed {
		if desired[name] {
			continue
		}
		log.Printf("service %s: no longer enabled, tearing down (%s)", name, serviceDir)
		tracker.ServiceAttempt(name)
		if err := deploy.Down(serviceDir); err != nil {
			errs = append(errs, fmt.Errorf("tearing down %s: %w", name, err))
			tracker.ServiceResult(name, err)
			continue
		}
		delete(st.deployed, name)
		delete(st.deployedOnce, name) // a later re-enable must force a fresh deploy
		stateDirty = true
		tracker.ServiceResult(name, nil)
		log.Printf("tore down %s (%s)", name, serviceDir)
	}

	var deployedCount, skipped int
	for _, svc := range st.svcCfg.EnabledServices() {
		serviceDir := filepath.Join(cfg.Git.ClonePath, svc.Path)

		if !fullReconcile && st.deployedOnce[svc.Name] && !serviceNeedsDeploy(cfg, res, svc) {
			skipped++
			continue
		}

		tracker.ServiceAttempt(svc.Name)

		log.Printf("service %s: decrypting secrets", svc.Name)
		if err := sopsdecrypt.DecryptServiceSecrets(serviceDir, svc.Name, cfg.Sops.SSHKeyPath, sopsdecrypt.DefaultSecretsBaseDir); err != nil {
			errs = append(errs, fmt.Errorf("decrypting secrets for %s: %w", svc.Name, err))
			tracker.ServiceResult(svc.Name, err)
			continue
		}

		log.Printf("service %s: running docker compose up", svc.Name)
		if err := deploy.Deploy(serviceDir); err != nil {
			errs = append(errs, fmt.Errorf("deploying %s: %w", svc.Name, err))
			tracker.ServiceResult(svc.Name, err)
			continue
		}
		tracker.ServiceResult(svc.Name, nil)

		st.deployedOnce[svc.Name] = true
		if st.deployed[svc.Name] != serviceDir {
			st.deployed[svc.Name] = serviceDir
			stateDirty = true
		}
		deployedCount++
		log.Printf("deployed %s (%s)", svc.Name, serviceDir)
	}

	if stateDirty {
		if err := state.Save(cfg.State.Path, st.deployed); err != nil {
			errs = append(errs, fmt.Errorf("saving deploy state %s: %w", cfg.State.Path, err))
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
func serviceNeedsDeploy(cfg *config.AgentConfig, res gitsync.Result, svc config.Service) bool {
	if !res.Changed {
		return false
	}
	changed, err := gitsync.PathChanged(cfg.Git.ClonePath, res.Before, res.After, svc.Path)
	if err != nil {
		log.Printf("service %s: couldn't tell whether %s changed, deploying to be safe: %v", svc.Name, svc.Path, err)
		return true
	}
	return changed
}
