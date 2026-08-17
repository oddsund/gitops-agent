// Package gitsync clones or pulls the config repo using go-git, so the
// agent never needs a git binary on the host.
package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

type Config struct {
	RepoURL   string
	Branch    string
	ClonePath string
}

// Result describes what a Sync did. Before is the zero hash for a fresh
// clone (there was no previous state to compare against); callers treat
// that as "everything is new".
type Result struct {
	Changed bool
	Before  plumbing.Hash
	After   plumbing.Hash
}

// SSHAuth loads an SSH keypair auth method from an ed25519 (or other)
// private key file, for use as the auth argument to Sync.
func SSHAuth(sshKeyPath string) (transport.AuthMethod, error) {
	auth, err := gitssh.NewPublicKeysFromFile("git", sshKeyPath, "")
	if err != nil {
		return nil, fmt.Errorf("loading SSH key %s: %w", sshKeyPath, err)
	}
	return auth, nil
}

// Sync ensures cfg.ClonePath contains a checkout of cfg.RepoURL at
// cfg.Branch, cloning it first if it doesn't exist yet, otherwise fetching
// and hard-resetting the worktree to origin/cfg.Branch. It reports whether
// HEAD moved as a result of this call (a fresh clone always counts as
// changed), along with the commits either side of the move. auth may be nil
// for unauthenticated remotes (e.g. local paths used in tests).
//
// The remote is treated as the sole source of truth: any local edit or
// commit made in the clone, or a force-pushed/rewritten remote history, is
// discarded rather than merged. This is deliberate GitOps posture -- the
// clone is disposable -- and it's what keeps a stray hand-edit in the clone
// from wedging reconciliation forever the way a failed merge pull would.
// The reset does not touch untracked files, so gitignored files such as
// decrypted secrets.env survive.
func Sync(cfg Config, auth transport.AuthMethod) (Result, error) {
	if _, statErr := os.Stat(cfg.ClonePath); errors.Is(statErr, os.ErrNotExist) {
		log.Printf("gitsync: %s doesn't exist yet, cloning %s (branch %s) -- this is a first run, or someone tidied up", cfg.ClonePath, cfg.RepoURL, cfg.Branch)
		repo, err := git.PlainClone(cfg.ClonePath, false, &git.CloneOptions{
			URL:           cfg.RepoURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
			SingleBranch:  true,
		})
		if err != nil {
			return Result{}, fmt.Errorf("cloning %s into %s: %w", cfg.RepoURL, cfg.ClonePath, err)
		}
		head, err := repo.Head()
		if err != nil {
			return Result{}, fmt.Errorf("reading HEAD after cloning into %s: %w", cfg.ClonePath, err)
		}
		log.Printf("gitsync: clone complete, at %s", short(head.Hash()))
		// Before stays zero: nothing to diff against, everything is new.
		return Result{Changed: true, After: head.Hash()}, nil
	} else if statErr != nil {
		return Result{}, fmt.Errorf("checking clone path %s: %w", cfg.ClonePath, statErr)
	}

	repo, err := git.PlainOpen(cfg.ClonePath)
	if err != nil {
		return Result{}, fmt.Errorf("opening repo at %s: %w", cfg.ClonePath, err)
	}

	headBefore, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("reading HEAD in %s: %w", cfg.ClonePath, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return Result{}, fmt.Errorf("getting worktree in %s: %w", cfg.ClonePath, err)
	}

	branchRef := plumbing.NewBranchReferenceName(cfg.Branch)
	refSpec := config.RefSpec(fmt.Sprintf("+%s:%s", branchRef, plumbing.NewRemoteReferenceName("origin", cfg.Branch)))
	err = repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      true,
		RefSpecs:   []config.RefSpec{refSpec},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Result{}, fmt.Errorf("fetching %s in %s: %w", cfg.RepoURL, cfg.ClonePath, err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", cfg.Branch), true)
	if err != nil {
		return Result{}, fmt.Errorf("resolving origin/%s in %s: %w", cfg.Branch, cfg.ClonePath, err)
	}

	// go-git's HardReset, unlike `git reset --hard`, deletes *every*
	// untracked file in the worktree, gitignored or not -- it diffs the
	// worktree against the index without applying gitignore rules. That
	// would silently wipe decrypted secrets.env files on every sync, so
	// back up untracked files first and restore whichever ones the reset
	// removes. wt.Clean() must never be added after this: that's the
	// go-git call that matches real git's untracked-file deletion, and is
	// exactly what we're routing around here.
	preserved, err := snapshotUntrackedFiles(repo, cfg.ClonePath)
	if err != nil {
		return Result{}, fmt.Errorf("snapshotting untracked files in %s: %w", cfg.ClonePath, err)
	}

	err = wt.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset})
	if err != nil {
		return Result{}, fmt.Errorf("resetting %s to origin/%s: %w", cfg.ClonePath, cfg.Branch, err)
	}

	if err := restoreMissingFiles(preserved); err != nil {
		return Result{}, fmt.Errorf("restoring untracked files in %s: %w", cfg.ClonePath, err)
	}

	headAfter, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("reading HEAD after reset in %s: %w", cfg.ClonePath, err)
	}

	res := Result{
		Changed: headBefore.Hash() != headAfter.Hash(),
		Before:  headBefore.Hash(),
		After:   headAfter.Hash(),
	}
	if !res.Changed {
		log.Printf("gitsync: synced %s, still at %s -- nothing new", cfg.ClonePath, short(res.After))
		return res, nil
	}
	log.Printf("gitsync: synced %s, %s -> %s", cfg.ClonePath, short(res.Before), short(res.After))
	return res, nil
}

func short(h plumbing.Hash) string { return h.String()[:12] }

// preservedFile is the saved content of an untracked file, keyed by its
// absolute path, so it can be written back if a hard reset deletes it.
type preservedFile struct {
	content []byte
	mode    fs.FileMode
}

// snapshotUntrackedFiles reads every untracked file in clonePath (tracked
// status per repo's index; gitignore rules are irrelevant here, since
// that's the whole point -- see the comment at the Reset call) into memory,
// so it can be restored after a hard reset deletes it.
func snapshotUntrackedFiles(repo *git.Repository, clonePath string) (map[string]preservedFile, error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("reading index: %w", err)
	}
	tracked := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		tracked[e.Name] = true
	}

	preserved := make(map[string]preservedFile)
	err = filepath.WalkDir(clonePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != clonePath && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(clonePath, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if tracked[rel] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		preserved[path] = preservedFile{content: content, mode: info.Mode()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return preserved, nil
}

// restoreMissingFiles rewrites any preserved file that no longer exists,
// i.e. one the hard reset deleted.
func restoreMissingFiles(preserved map[string]preservedFile) error {
	for path, f := range preserved {
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("recreating directory for %s: %w", path, err)
		}
		if err := os.WriteFile(path, f.content, f.mode); err != nil {
			return fmt.Errorf("restoring %s: %w", path, err)
		}
		log.Printf("gitsync: restored untracked file %s after reset", path)
	}
	return nil
}
