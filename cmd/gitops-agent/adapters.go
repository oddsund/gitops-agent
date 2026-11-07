package main

import (
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/reconcile"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
)

// These adapters bind configuration (repo URL, clone path, SSH key, secrets
// directory) that the reconcile interfaces deliberately do not carry. They
// live here so internal/reconcile never imports deploy or sopsdecrypt. Keep
// them pure pass-throughs: config.LoadServices, for one, logs its own line.

var (
	_ reconcile.Syncer         = gitSyncer{}
	_ reconcile.ChangeDetector = gitChanges{}
	_ reconcile.ServiceLoader  = manifestLoader{}
	_ reconcile.Decrypter      = sopsDecrypter{}
	_ reconcile.Deployer       = composeDeployer{}
)

type gitSyncer struct {
	cfg  gitsync.Config
	auth transport.AuthMethod
}

func (s gitSyncer) Sync() (gitsync.Result, error) { return gitsync.Sync(s.cfg, s.auth) }

type gitChanges struct{ clonePath string }

func (c gitChanges) PathChanged(res gitsync.Result, path string) (bool, error) {
	return gitsync.PathChanged(c.clonePath, res.Before, res.After, path)
}

type manifestLoader struct{}

func (manifestLoader) LoadServices(path string) (*config.ServicesConfig, error) {
	return config.LoadServices(path)
}

type sopsDecrypter struct{ keyPath, baseDir string }

func (d sopsDecrypter) DecryptServiceSecrets(serviceDir, serviceName string) error {
	return sopsdecrypt.DecryptServiceSecrets(serviceDir, serviceName, d.keyPath, d.baseDir)
}

func (d sopsDecrypter) RemoveServiceSecrets(serviceName string) error {
	return sopsdecrypt.RemoveServiceSecrets(serviceName, d.baseDir)
}

type composeDeployer struct{}

func (composeDeployer) Deploy(dir string) error { return deploy.Deploy(dir) }
func (composeDeployer) Down(dir string) error   { return deploy.Down(dir) }
