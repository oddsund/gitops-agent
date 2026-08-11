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

func main() {
	configPath := flag.String("config", "/etc/gitops-agent/config.toml", "path to config.toml")
	flag.Parse()

	log.Printf("gitops-agent starting up, config: %s", *configPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	if len(cfg.EnabledServices()) == 0 {
		log.Printf("no services enabled -- I'll keep polling regardless, just won't have anything to deploy")
	}

	auth, err := gitsync.SSHAuth(cfg.Sops.SSHKeyPath)
	if err != nil {
		log.Fatalf("loading SSH key: %v", err)
	}

	interval := time.Duration(cfg.Git.PullIntervalSeconds) * time.Second
	log.Printf("polling %s (branch %s) every %s", cfg.Git.RepoURL, cfg.Git.Branch, interval)

	for {
		if err := runOnce(cfg, auth); err != nil {
			log.Printf("run failed, will try again next cycle: %v", err)
		}
		time.Sleep(interval)
	}
}

// runOnce syncs the config repo, then decrypts and deploys every enabled
// service; one service's failure doesn't stop the others.
func runOnce(cfg *config.Config, auth transport.AuthMethod) error {
	changed, err := gitsync.Sync(gitsync.Config{
		RepoURL:   cfg.Git.RepoURL,
		Branch:    cfg.Git.Branch,
		ClonePath: cfg.Git.ClonePath,
	}, auth)
	if err != nil {
		return fmt.Errorf("syncing %s: %w", cfg.Git.RepoURL, err)
	}
	log.Printf("git sync complete (changed=%v)", changed)

	var errs []error
	for _, svc := range cfg.EnabledServices() {
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
	return errors.Join(errs...)
}
