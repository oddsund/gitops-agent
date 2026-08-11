package gitsync

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRemote creates a local git repo with one commit, used as a stand-in
// remote for Sync (auth is nil, so the local filesystem transport is used).
func newRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestSync_ClonesWhenAbsent(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	changed, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on first clone")
	}
	if _, err := os.Stat(filepath.Join(clonePath, "file.txt")); err != nil {
		t.Errorf("expected file.txt in clone: %v", err)
	}
}

func TestSync_NoChangeThenPullsNewCommit(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	changed, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync (no change): %v", err)
	}
	if changed {
		t.Error("expected changed=false when remote has no new commits")
	}

	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "commit", "-am", "update")

	changed, err = Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync (after change): %v", err)
	}
	if !changed {
		t.Error("expected changed=true after new commit on remote")
	}

	content, err := os.ReadFile(filepath.Join(clonePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "v2" {
		t.Errorf("content = %q, want v2", content)
	}
}

func TestSync_MissingClonePathParent(t *testing.T) {
	remote := newRemote(t)
	// A path several levels deep under a nonexistent parent should still
	// work: PlainClone creates intermediate directories.
	clonePath := filepath.Join(t.TempDir(), "a", "b", "c")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}
