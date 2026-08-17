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

	res, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true on first clone")
	}
	if !res.Before.IsZero() {
		t.Errorf("Before = %s, want the zero hash on a fresh clone", res.Before)
	}
	if res.After.IsZero() {
		t.Error("After is the zero hash, want the cloned HEAD")
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

	res, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync (no change): %v", err)
	}
	if res.Changed {
		t.Error("expected Changed=false when remote has no new commits")
	}
	if res.Before != res.After {
		t.Errorf("Before (%s) != After (%s) with no new commits", res.Before, res.After)
	}

	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "commit", "-am", "update")

	res, err = Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync (after change): %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true after new commit on remote")
	}
	if res.Before == res.After {
		t.Error("Before and After are equal after a new commit, want them to differ")
	}

	content, err := os.ReadFile(filepath.Join(clonePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "v2" {
		t.Errorf("content = %q, want v2", content)
	}
}

func TestSync_RecoversFromLocallyModifiedTrackedFile(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	// Simulate a stray hand-edit in the clone: a modification to a tracked
	// file, with no commit. A Pull-based Sync would fail hard on this.
	if err := os.WriteFile(filepath.Join(clonePath, "file.txt"), []byte("locally edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "commit", "-am", "update")

	res, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync after local edit: %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true after new remote commit")
	}

	content, err := os.ReadFile(filepath.Join(clonePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "v2" {
		t.Errorf("content = %q, want v2 -- local edit should have been discarded", content)
	}
}

func TestSync_RecoversFromLocalCommit(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	// Simulate a commit made directly on the host, diverging from origin.
	if err := os.WriteFile(filepath.Join(clonePath, "file.txt"), []byte("local commit"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clonePath, "commit", "-am", "stray local commit")

	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "commit", "-am", "update")

	res, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync after local commit: %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true after new remote commit")
	}

	content, err := os.ReadFile(filepath.Join(clonePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "v2" {
		t.Errorf("content = %q, want v2 -- local commit should have been discarded", content)
	}
}

func TestSync_FollowsForcePushedRemote(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	// Rewrite the remote's history: amend the initial commit rather than
	// adding on top of it, then force it into place.
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("rewritten"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "add", "file.txt")
	runGit(t, remote, "commit", "--amend", "-m", "rewritten history")

	res, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil)
	if err != nil {
		t.Fatalf("Sync after force-pushed remote: %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true after rewritten remote history")
	}

	content, err := os.ReadFile(filepath.Join(clonePath, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "rewritten" {
		t.Errorf("content = %q, want rewritten -- clone should follow the rewritten history", content)
	}
}

func TestSync_PreservesUntrackedGitignoredFile(t *testing.T) {
	remote := newRemote(t)
	clonePath := filepath.Join(t.TempDir(), "clone")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}

	// Stand in for a decrypted secrets.env: untracked and gitignored.
	if err := os.WriteFile(filepath.Join(clonePath, ".gitignore"), []byte("secrets.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(clonePath, "secrets.env")
	if err := os.WriteFile(secretPath, []byte("TOKEN=shh"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "commit", "-am", "update")

	if _, err := Sync(Config{RepoURL: remote, Branch: "main", ClonePath: clonePath}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	content, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("secrets.env should survive a hard reset: %v", err)
	}
	if string(content) != "TOKEN=shh" {
		t.Errorf("secrets.env content = %q, want unchanged", content)
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
