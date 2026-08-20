package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestIsSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bare invocation", nil, false},
		{"config flag", []string{"-config", "/etc/gitops-agent/config.toml"}, false},
		{"version flag", []string{"-version"}, false},
		{"unknown subcommand", []string{"update"}, true},
		{"subcommand with args", []string{"install", "--force"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSubcommand(tc.args); got != tc.want {
				t.Errorf("isSubcommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestDispatchUnknownSubcommand(t *testing.T) {
	var stderr bytes.Buffer
	code := dispatch(&stderr, "bogus", nil)

	if code == 0 {
		t.Fatalf("dispatch returned 0 for an unknown subcommand, want non-zero")
	}
	if !strings.Contains(stderr.String(), "bogus") || !strings.Contains(stderr.String(), "usage") {
		t.Errorf("stderr = %q, want it to name the subcommand and print usage", stderr.String())
	}
}

func TestDispatchRegisteredSubcommand(t *testing.T) {
	var called []string
	subcommands["fake"] = func(args []string) error {
		called = args
		return nil
	}
	defer delete(subcommands, "fake")

	var stderr bytes.Buffer
	code := dispatch(&stderr, "fake", []string{"a", "b"})

	if code != 0 {
		t.Errorf("dispatch returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
	if len(called) != 2 || called[0] != "a" || called[1] != "b" {
		t.Errorf("subcommand called with %v, want [a b]", called)
	}
}

func TestDispatchSubcommandError(t *testing.T) {
	subcommands["fake"] = func(args []string) error {
		return errors.New("boom")
	}
	defer delete(subcommands, "fake")

	var stderr bytes.Buffer
	code := dispatch(&stderr, "fake", nil)

	if code == 0 {
		t.Fatalf("dispatch returned 0 for a failing subcommand, want non-zero")
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want it to contain the subcommand's error", stderr.String())
	}
}
