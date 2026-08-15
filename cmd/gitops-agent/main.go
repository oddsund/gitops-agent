// Command gitops-agent is a long-running process that periodically syncs
// this repo, decrypts each enabled service's secrets, and deploys it via
// docker compose.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
)

// servicesManifest is the services list's path relative to the repo root.
// It's tracked in git, unlike config.toml, so enabling a service is a
// commit, not an SSH session.
const servicesManifest = "services.toml"

func main() {
	configPath := flag.String("config", "/etc/gitops-agent/config.toml", "path to config.toml")
	flag.Parse()

	log.Printf("gitops-agent starting up, config: %s", *configPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	auth, err := gitsync.SSHAuth(cfg.Sops.SSHKeyPath)
	if err != nil {
		log.Fatalf("loading SSH key: %v", err)
	}

	interval := time.Duration(cfg.Git.PullIntervalSeconds) * time.Second
	log.Printf("polling %s (branch %s) every %s", cfg.Git.RepoURL, cfg.Git.Branch, interval)

	// svcCfg holds the last-known-good services manifest across cycles: if
	// a pulled commit has a broken services.toml, we keep deploying what
	// we last successfully parsed instead of grinding to a halt.
	var svcCfg *config.ServicesConfig

	for {
		svcCfg, err = runOnce(cfg, svcCfg, auth)
		if err != nil {
			log.Printf("run failed, will try again next cycle: %v", err)
		}
		time.Sleep(interval)
	}
}

// runOnce syncs the config repo, reloads the services manifest, then
// decrypts and deploys every enabled service; one service's failure
// doesn't stop the others. It returns the services manifest to use next
// cycle: the freshly reloaded one, or prevSvcCfg unchanged if the manifest
// in the repo failed to parse.
func runOnce(cfg *config.AgentConfig, prevSvcCfg *config.ServicesConfig, auth transport.AuthMethod) (*config.ServicesConfig, error) {
	changed, err := gitsync.Sync(gitsync.Config{
		RepoURL:   cfg.Git.RepoURL,
		Branch:    cfg.Git.Branch,
		ClonePath: cfg.Git.ClonePath,
	}, auth)
	if err != nil {
		return prevSvcCfg, fmt.Errorf("syncing %s: %w", cfg.Git.RepoURL, err)
	}
	log.Printf("git sync complete (changed=%v)", changed)

	svcCfg := prevSvcCfg
	manifestPath := filepath.Join(cfg.Git.ClonePath, servicesManifest)
	if loaded, err := config.LoadServices(manifestPath); err != nil {
		log.Printf("services manifest %s is broken, keeping last-known-good list: %v", manifestPath, err)
	} else {
		svcCfg = loaded
	}

	if svcCfg == nil {
		log.Printf("no valid services manifest loaded yet -- nothing to deploy this cycle")
		return svcCfg, nil
	}

	var errs []error
	for _, svc := range svcCfg.EnabledServices() {
		serviceDir := filepath.Join(cfg.Git.ClonePath, svc.Path)

		log.Printf("service %s: decrypting secrets", svc.Name)
		if err := sopsdecrypt.DecryptServiceSecrets(serviceDir, cfg.Sops.SSHKeyPath); err != nil {
			errs = append(errs, fmt.Errorf("decrypting secrets for %s: %w", svc.Name, err))
			continue
		}

		log.Printf("service %s: running docker compose up", svc.Name)
		if err := deploy.Deploy(serviceDir); err != nil {
			errs = append(errs, fmt.Errorf("deploying %s: %w", svc.Name, err))
			continue
		}

		log.Printf("deployed %s (%s)", svc.Name, serviceDir)
	}
	return svcCfg, errors.Join(errs...)
}
