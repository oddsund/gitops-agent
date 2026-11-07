package main

import (
	"log/slog"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/oddsund/gitops-agent/internal/config"
	"github.com/oddsund/gitops-agent/internal/deploy"
	"github.com/oddsund/gitops-agent/internal/gitsync"
	"github.com/oddsund/gitops-agent/internal/notify"
	"github.com/oddsund/gitops-agent/internal/reconcile"
	"github.com/oddsund/gitops-agent/internal/sopsdecrypt"
)

// These adapters bind configuration (repo URL, clone path, SSH key, secrets
// directory) that the reconcile interfaces deliberately do not carry. They
// live here so internal/reconcile never imports deploy or sopsdecrypt. Keep
// them pure pass-throughs.

var (
	_ reconcile.Syncer         = gitSyncer{}
	_ reconcile.ChangeDetector = gitChanges{}
	_ reconcile.ServiceLoader  = manifestLoader{}
	_ reconcile.Decrypter      = sopsDecrypter{}
	_ reconcile.Deployer       = composeDeployer{}
)

// notifyConfig converts the [notify] section into the notify package's
// plain-value config. hostname is this machine's name.
func notifyConfig(nc config.NotifyConfig, hostname string) notify.Config {
	return notify.Config{
		Host:         nc.SMTPHost,
		Port:         nc.SMTPPort,
		TLS:          nc.TLS,
		From:         nc.From,
		To:           nc.To,
		Username:     nc.Username,
		PasswordFile: nc.PasswordFile,
		MinInterval:  time.Duration(nc.MinIntervalSeconds) * time.Second,
		IncludeError: nc.IncludeError,
		StatusURL:    nc.StatusURL,
		Hostname:     hostname,
	}
}

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

type sopsDecrypter struct {
	keyPath, baseDir string
	log              *slog.Logger
}

func (d sopsDecrypter) DecryptServiceSecrets(serviceDir, serviceName string) error {
	return sopsdecrypt.DecryptServiceSecrets(serviceDir, serviceName, d.keyPath, d.baseDir, d.log)
}

func (d sopsDecrypter) RemoveServiceSecrets(serviceName string) error {
	return sopsdecrypt.RemoveServiceSecrets(serviceName, d.baseDir, d.log)
}

type composeDeployer struct{ log *slog.Logger }

func (d composeDeployer) Deploy(dir string) error { return deploy.Deploy(dir, d.log) }
func (d composeDeployer) Down(dir string) error   { return deploy.Down(dir, d.log) }
