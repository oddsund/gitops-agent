package main

import (
	"bytes"
	"errors"
	"flag"
	"os/user"
	"strings"
	"testing"
)

func fakeLookup(name string) (*user.User, error) {
	switch name {
	case "nohome":
		return &user.User{Username: name}, nil
	case "relhome":
		return &user.User{Username: name, HomeDir: "home/x"}, nil
	}
	if name == "ghost" {
		return nil, user.UnknownUserError(name)
	}
	return &user.User{Username: name, HomeDir: "/home/" + name}, nil
}

func TestInstall_Registered(t *testing.T) {
	if subcommands["install"] == nil {
		t.Fatal("install subcommand is not registered")
	}
}

func TestNewInstallConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
		user    string
		ssh     string
		clone   string
		repo    string
	}{
		{name: "defaults", args: []string{"--repo-url", "X", "--user", "pi"},
			user: "pi", ssh: "/home/pi/.ssh/id_ed25519", clone: "/opt/gitops-agent/repo", repo: "X"},
		{name: "sudo user", args: []string{"--repo-url", "X"}, env: map[string]string{"SUDO_USER": "alice"},
			user: "alice", ssh: "/home/alice/.ssh/id_ed25519", clone: "/opt/gitops-agent/repo", repo: "X"},
		{name: "flag beats sudo user", args: []string{"--user", "pi"}, env: map[string]string{"SUDO_USER": "alice"},
			user: "pi", ssh: "/home/pi/.ssh/id_ed25519", clone: "/opt/gitops-agent/repo"},
		{name: "passthrough", args: []string{"--user", "pi", "--ssh-key", "/k", "--clone-path", "/c"},
			user: "pi", ssh: "/k", clone: "/c"},
		{name: "empty home", args: []string{"--user", "nohome"}, user: "nohome", ssh: "", clone: "/opt/gitops-agent/repo"},
		{name: "relative home", args: []string{"--user", "relhome"}, user: "relhome", ssh: "", clone: "/opt/gitops-agent/repo"},
		{name: "no user", args: []string{"--repo-url", "X"}, wantErr: "--user"},
		{name: "unknown user", args: []string{"--user", "ghost"}, wantErr: "ghost"},
		{name: "positional", args: []string{"--user", "pi", "extra"}, wantErr: "install takes no arguments"},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: "bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			cfg, err := newInstallConfig(tc.args, env(tc.env), fakeLookup, &stderr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.User != tc.user || cfg.SSHKeyPath != tc.ssh || cfg.ClonePath != tc.clone || cfg.RepoURL != tc.repo {
				t.Errorf("cfg = %+v", cfg)
			}
			if cfg.Version != version {
				t.Errorf("Version = %q, want %q", cfg.Version, version)
			}
		})
	}

	t.Run("help", func(t *testing.T) {
		var stderr bytes.Buffer
		_, err := newInstallConfig([]string{"-h"}, env(nil), fakeLookup, &stderr)
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("err = %v, want flag.ErrHelp", err)
		}
		if !strings.Contains(stderr.String(), "usage: gitops-agent install") {
			t.Errorf("stderr = %q", stderr.String())
		}
	})
}
