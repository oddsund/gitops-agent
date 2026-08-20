package main

import (
	"strings"
	"testing"
)

func TestRunInstall_RequiresRepoURL(t *testing.T) {
	err := runInstall(nil)
	if err == nil || !strings.Contains(err.Error(), "-repo-url") {
		t.Fatalf("runInstall with no -repo-url = %v, want an error naming -repo-url", err)
	}
}

func TestRunInstall_RejectsUnknownFlag(t *testing.T) {
	if err := runInstall([]string{"-repo-url=git@github.com:x/y.git", "-bogus"}); err == nil {
		t.Fatal("runInstall with an unknown flag should return an error, got nil")
	}
}
