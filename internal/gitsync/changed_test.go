package gitsync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// writeCommit writes files (path -> contents, relative to dir) into the
// repo at dir and commits them, returning the new HEAD hash.
func writeCommit(t *testing.T, dir string, msg string, files map[string]string) plumbing.Hash {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", msg)

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	return head.Hash()
}

// newRepoWithServices builds a repo shaped like the real one: two service
// directories, each with a compose file.
func newRepoWithServices(t *testing.T) (dir string, first plumbing.Hash) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	first = writeCommit(t, dir, "initial", map[string]string{
		"services/demoapp/compose.yml":  "services: {demoapp: {}}\n",
		"services/thirdapp/compose.yml": "services: {thirdapp: {}}\n",
		"services.toml":                 "# manifest\n",
	})
	return dir, first
}

func TestPathChanged_TouchedService(t *testing.T) {
	dir, before := newRepoWithServices(t)
	after := writeCommit(t, dir, "tweak demoapp", map[string]string{
		"services/demoapp/compose.yml": "services: {demoapp: {restart: always}}\n",
	})

	changed, err := PathChanged(dir, before, after, "services/demoapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if !changed {
		t.Error("changed = false for the service that was edited, want true")
	}
}

func TestPathChanged_UntouchedService(t *testing.T) {
	dir, before := newRepoWithServices(t)
	after := writeCommit(t, dir, "tweak demoapp", map[string]string{
		"services/demoapp/compose.yml": "services: {demoapp: {restart: always}}\n",
	})

	// This is the whole point: editing demoapp must not redeploy thirdapp.
	changed, err := PathChanged(dir, before, after, "services/thirdapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if changed {
		t.Error("changed = true for an untouched service, want false")
	}
}

func TestPathChanged_UnrelatedFileOutsideServices(t *testing.T) {
	dir, before := newRepoWithServices(t)
	after := writeCommit(t, dir, "edit the manifest", map[string]string{
		"services.toml": "# manifest, now with feeling\n",
	})

	changed, err := PathChanged(dir, before, after, "services/demoapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if changed {
		t.Error("changed = true when only services.toml moved, want false")
	}
}

func TestPathChanged_NewlyAddedService(t *testing.T) {
	dir, before := newRepoWithServices(t)
	after := writeCommit(t, dir, "add otherapp", map[string]string{
		"services/otherapp/compose.yml": "services: {otherapp: {}}\n",
	})

	changed, err := PathChanged(dir, before, after, "services/otherapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if !changed {
		t.Error("changed = false for a service that didn't exist before, want true")
	}
}

func TestPathChanged_RemovedService(t *testing.T) {
	dir, before := newRepoWithServices(t)
	if err := os.RemoveAll(filepath.Join(dir, "services", "thirdapp")); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "drop thirdapp")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	changed, err := PathChanged(dir, before, head.Hash(), "services/thirdapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if !changed {
		t.Error("changed = false for a removed service, want true")
	}
}

func TestPathChanged_AbsentFromBothCommits(t *testing.T) {
	dir, before := newRepoWithServices(t)
	after := writeCommit(t, dir, "unrelated", map[string]string{
		"services.toml": "# still here\n",
	})

	changed, err := PathChanged(dir, before, after, "services/never-existed")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if changed {
		t.Error("changed = true for a path in neither commit, want false")
	}
}

func TestPathChanged_FreshCloneCountsAsChanged(t *testing.T) {
	dir, after := newRepoWithServices(t)

	// Sync returns a zero Before after cloning; everything is new then.
	changed, err := PathChanged(dir, plumbing.ZeroHash, after, "services/demoapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if !changed {
		t.Error("changed = false with a zero Before hash, want true")
	}
}

func TestPathChanged_SameCommitBothSides(t *testing.T) {
	dir, only := newRepoWithServices(t)

	changed, err := PathChanged(dir, only, only, "services/demoapp")
	if err != nil {
		t.Fatalf("PathChanged: %v", err)
	}
	if changed {
		t.Error("changed = true comparing a commit against itself, want false")
	}
}

func TestPathChanged_UnknownCommitErrors(t *testing.T) {
	dir, after := newRepoWithServices(t)
	bogus := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")

	// Callers fail open on error, so this must report an error rather than
	// quietly claiming nothing changed.
	if _, err := PathChanged(dir, bogus, after, "services/demoapp"); err == nil {
		t.Error("PathChanged with an unknown commit returned nil error, want an error")
	}
}
