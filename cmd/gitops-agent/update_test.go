package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/oddsund/gitops-agent/internal/selfupdate"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestUpdateRegistered(t *testing.T) {
	if subcommands["update"] == nil {
		t.Fatal("update is not registered")
	}
}

func TestUpdateDefaults(t *testing.T) {
	cfg, err := newUpdateConfig(nil, envFrom(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BinPath != "/usr/local/bin/gitops-agent" ||
		cfg.VersionFilePath != "/etc/gitops-agent/installed-version" ||
		cfg.ServiceName != "gitops-agent" ||
		cfg.Repo != "oddsund/gitops-agent" ||
		cfg.Force || cfg.SkipAttestation {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestUpdateEnvOverrides(t *testing.T) {
	cfg, err := newUpdateConfig(nil, envFrom(map[string]string{
		"GITOPS_AGENT_BIN":          "/b",
		"GITOPS_AGENT_VERSION_FILE": "/v",
		"GITOPS_AGENT_SERVICE":      "svc",
		"GITOPS_AGENT_REPO":         "o/r",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BinPath != "/b" || cfg.VersionFilePath != "/v" || cfg.ServiceName != "svc" || cfg.Repo != "o/r" {
		t.Errorf("cfg = %+v", cfg)
	}

	for value, want := range map[string]bool{"1": true, "0": false, "true": false, "": false} {
		cfg, err := newUpdateConfig(nil, envFrom(map[string]string{"GITOPS_AGENT_SKIP_ATTESTATION": value}), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SkipAttestation != want {
			t.Errorf("SKIP_ATTESTATION=%q: SkipAttestation = %v, want %v", value, cfg.SkipAttestation, want)
		}
	}
}

func TestUpdateFlagsBeatEnv(t *testing.T) {
	cfg, err := newUpdateConfig(
		[]string{"--bin", "/x", "--version-file", "/y", "--service", "z", "--force"},
		envFrom(map[string]string{
			"GITOPS_AGENT_BIN":          "/b",
			"GITOPS_AGENT_VERSION_FILE": "/v",
			"GITOPS_AGENT_SERVICE":      "svc",
		}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BinPath != "/x" || cfg.VersionFilePath != "/y" || cfg.ServiceName != "z" || !cfg.Force {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestReadToken(t *testing.T) {
	var readPath string
	tok, err := readToken(envFrom(map[string]string{"GITHUB_TOKEN_FILE": "/tmp/tok"}), func(p string) ([]byte, error) {
		readPath = p
		return []byte("tok\n"), nil
	})
	if err != nil || tok != "tok" || readPath != "/tmp/tok" {
		t.Errorf("token = %q, path = %q, err = %v", tok, readPath, err)
	}

	tok, err = readToken(envFrom(nil), func(p string) ([]byte, error) {
		readPath = p
		return nil, fs.ErrNotExist
	})
	if err != nil || tok != "" || readPath != "/etc/gitops-agent/github-token" {
		t.Errorf("missing token file: token = %q, path = %q, err = %v", tok, readPath, err)
	}

	_, err = readToken(envFrom(nil), func(string) ([]byte, error) { return nil, fs.ErrPermission })
	if err == nil || !strings.Contains(err.Error(), "reading token file") {
		t.Errorf("err = %v", err)
	}
}

func TestUpdateRejectsPositional(t *testing.T) {
	_, err := newUpdateConfig([]string{"now"}, envFrom(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "update takes no arguments") {
		t.Errorf("err = %v", err)
	}
}

func TestUpdateUnknownFlagIsNotPrinted(t *testing.T) {
	var stderr bytes.Buffer
	_, err := newUpdateConfig([]string{"--bogus"}, envFrom(nil), &stderr)
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("err = %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: dispatch prints the error once", stderr.String())
	}
}

func TestUpdateHelpPrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	_, err := newUpdateConfig([]string{"-h"}, envFrom(nil), &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "usage: gitops-agent update") || !strings.Contains(stderr.String(), "-force") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunUpdateHelpIsSuccess(t *testing.T) {
	if err := runUpdate([]string{"-h"}); err != nil {
		t.Errorf("runUpdate(-h) = %v, want nil", err)
	}
}

func TestTokenPath(t *testing.T) {
	if got := tokenPath(envFrom(map[string]string{"GITHUB_TOKEN_FILE": "/tmp/tok"})); got != "/tmp/tok" {
		t.Errorf("override: got %q", got)
	}
	if got := tokenPath(envFrom(nil)); got != selfupdate.DefaultTokenFile {
		t.Errorf("default: got %q", got)
	}
}
