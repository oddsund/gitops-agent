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
// always counts as changed). auth may be nil for unauthenticated remotes
// (e.g. local paths used in tests).
func Sync(cfg Config, auth transport.AuthMethod) (changed bool, err error) {
	if _, statErr := os.Stat(cfg.ClonePath); errors.Is(statErr, os.ErrNotExist) {
		log.Printf("gitsync: %s doesn't exist yet, cloning %s (branch %s) -- this is a first run, or someone tidied up", cfg.ClonePath, cfg.RepoURL, cfg.Branch)
		_, err := git.PlainClone(cfg.ClonePath, false, &git.CloneOptions{
			URL:           cfg.RepoURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
			SingleBranch:  true,
		})
		if err != nil {
			return false, fmt.Errorf("cloning %s into %s: %w", cfg.RepoURL, cfg.ClonePath, err)
		}
		log.Printf("gitsync: clone complete")
		return true, nil
	} else if statErr != nil {
		return false, fmt.Errorf("checking clone path %s: %w", cfg.ClonePath, statErr)
	}

	repo, err := git.PlainOpen(cfg.ClonePath)
	if err != nil {
		return false, fmt.Errorf("opening repo at %s: %w", cfg.ClonePath, err)
	}

	headBefore, err := repo.Head()
	if err != nil {
		return false, fmt.Errorf("reading HEAD in %s: %w", cfg.ClonePath, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("getting worktree in %s: %w", cfg.ClonePath, err)
	}

	err = wt.Pull(&git.PullOptions{
		RemoteName:    "origin",
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
		SingleBranch:  true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return false, fmt.Errorf("pulling %s in %s: %w", cfg.RepoURL, cfg.ClonePath, err)
	}

	headAfter, err := repo.Head()
	if err != nil {
		return false, fmt.Errorf("reading HEAD after pull in %s: %w", cfg.ClonePath, err)
	}

	if headBefore.Hash() == headAfter.Hash() {
		log.Printf("gitsync: pulled %s, still at %s -- nothing new", cfg.ClonePath, headAfter.Hash().String()[:12])
		return false, nil
	}
	log.Printf("gitsync: pulled %s, %s -> %s", cfg.ClonePath, headBefore.Hash().String()[:12], headAfter.Hash().String()[:12])
	return true, nil
}
