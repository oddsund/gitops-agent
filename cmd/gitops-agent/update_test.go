package main

import "testing"

func TestRunUpdate_RejectsArguments(t *testing.T) {
	if err := runUpdate([]string{"--force"}); err == nil {
		t.Fatal("runUpdate with arguments should return an error, got nil")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("GA_TEST_ENV_OR", "")
	if got := envOr("GA_TEST_ENV_OR", "default"); got != "default" {
		t.Errorf("envOr with unset var = %q, want %q", got, "default")
	}

	t.Setenv("GA_TEST_ENV_OR", "set")
	if got := envOr("GA_TEST_ENV_OR", "default"); got != "set" {
		t.Errorf("envOr with set var = %q, want %q", got, "set")
	}
}
