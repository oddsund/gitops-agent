// Command gitops-agent is a one-shot runner for now: it loads config,
// syncs the config repo, and decrypts each enabled service's secrets.
// Deploying the services themselves (docker compose) and the run loop
// land in a later change.
package main

import (
	"flag"
	"log"
	"path/filepath"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
)

func main() {
	configPath := flag.String("config", "/etc/gitops-agent/config.toml", "path to config.toml")
	flag.Parse()

	log.Printf("gitops-agent starting up (one-shot for now), config: %s", *configPath)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	auth, err := gitsync.SSHAuth(cfg.Sops.SSHKeyPath)
	if err != nil {
		log.Fatalf("loading SSH key: %v", err)
	}

	changed, err := gitsync.Sync(gitsync.Config{
		RepoURL:   cfg.Git.RepoURL,
		Branch:    cfg.Git.Branch,
		ClonePath: cfg.Git.ClonePath,
	}, auth)
	if err != nil {
		log.Fatalf("syncing %s: %v", cfg.Git.RepoURL, err)
	}
	log.Printf("git sync complete (changed=%v)", changed)

	for _, svc := range cfg.EnabledServices() {
		serviceDir := filepath.Join(cfg.Git.ClonePath, svc.Path)
		log.Printf("service %s: decrypting secrets", svc.Name)
		if err := sopsdecrypt.DecryptServiceSecrets(serviceDir, cfg.Sops.SSHKeyPath); err != nil {
			log.Fatalf("decrypting secrets for %s: %v", svc.Name, err)
		}
		log.Printf("decrypted secrets for %s (%s)", svc.Name, serviceDir)
	}

	log.Printf("done for now -- config, sync and decrypt all work, deploying isn't wired up yet")
}
