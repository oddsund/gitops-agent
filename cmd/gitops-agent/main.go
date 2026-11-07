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
	"syscall"
	"time"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/reconcile"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
	"github.com/oddsund/gitops-agent/internal/statusserver"
)

// version is set at build time via -ldflags "-X main.version=..." (see
// .github/workflows/release.yml), which passes the release
// tag -- the exact string `gitops-agent update` also writes to installed-version, so
// a provisioning check can compare the two with a plain string equality
// check. A plain `go build` leaves it at "dev".
var version = "dev"

func main() {
	if isSubcommand(os.Args[1:]) {
		os.Exit(dispatch(os.Stderr, os.Args[1], os.Args[2:]))
	}

	flag.Usage = printDaemonUsage
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

	r := &reconcile.Reconciler{
		Config: reconcile.Config{
			RepoURL:            cfg.Git.RepoURL,
			Branch:             cfg.Git.Branch,
			ClonePath:          cfg.Git.ClonePath,
			StatePath:          cfg.State.Path,
			Idle:               time.Duration(cfg.Git.PullIntervalSeconds) * time.Second,
			Active:             time.Duration(cfg.Git.ActiveIntervalSeconds) * time.Second,
			ActiveWindow:       time.Duration(cfg.Git.ActiveWindowSeconds) * time.Second,
			FullReconcileEvery: time.Duration(cfg.Git.FullReconcileIntervalSeconds) * time.Second,
		},
		Syncer: gitSyncer{
			cfg:  gitsync.Config{RepoURL: cfg.Git.RepoURL, Branch: cfg.Git.Branch, ClonePath: cfg.Git.ClonePath},
			auth: auth,
		},
		Changes:    gitChanges{clonePath: cfg.Git.ClonePath},
		Services:   manifestLoader{},
		Decrypter:  sopsDecrypter{keyPath: cfg.Sops.SSHKeyPath, baseDir: sopsdecrypt.DefaultSecretsBaseDir},
		Deployer:   composeDeployer{},
		Tracker:    tracker,
		StatusFile: statusserver.DefaultStatusFilePath,
	}
	if err := r.Run(ctx, reloadCh); err != nil {
		log.Fatalf("gitops-agent: %v", err)
	}
	log.Printf("gitops-agent: shutting down")
}

// printDaemonUsage is what -h prints. It lists the subcommands because
// systemd/update.bash probes `gitops-agent -h` to tell a binary that has
// the update subcommand from one that predates it. -h is the probe because
// every build parses flags before it starts the daemon, so asking an old
// binary for help is safe, whereas `gitops-agent update` on an old binary
// starts the reconcile loop.
func printDaemonUsage() {
	out := flag.CommandLine.Output()
	usage(out)
	fmt.Fprintln(out, "\nflags:")
	flag.PrintDefaults()
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
