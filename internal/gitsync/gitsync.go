// Package gitsync clones or pulls the config repo using go-git, so the
// agent never needs a git binary on the host.
package gitsync

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/go-git/go-git/v5"
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
// cfg.Branch, cloning it first if it doesn't exist yet, otherwise pulling.
// It reports whether HEAD moved as a result of this call (a fresh clone
// always counts as changed), along with the commits either side of the
// move. auth may be nil for unauthenticated remotes (e.g. local paths used
// in tests).
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

	err = wt.Pull(&git.PullOptions{
		RemoteName:    "origin",
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
		SingleBranch:  true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Result{}, fmt.Errorf("pulling %s in %s: %w", cfg.RepoURL, cfg.ClonePath, err)
	}

	headAfter, err := repo.Head()
	if err != nil {
		return Result{}, fmt.Errorf("reading HEAD after pull in %s: %w", cfg.ClonePath, err)
	}

	res := Result{
		Changed: headBefore.Hash() != headAfter.Hash(),
		Before:  headBefore.Hash(),
		After:   headAfter.Hash(),
	}
	if !res.Changed {
		log.Printf("gitsync: pulled %s, still at %s -- nothing new", cfg.ClonePath, short(res.After))
		return res, nil
	}
	log.Printf("gitsync: pulled %s, %s -> %s", cfg.ClonePath, short(res.Before), short(res.After))
	return res, nil
}

func short(h plumbing.Hash) string { return h.String()[:12] }
