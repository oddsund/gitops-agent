// Package gitsync clones or pulls the config repo using go-git, so the
// agent never needs a git binary on the host.
package gitsync

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	// Log receives gitsync's log lines. Nil discards.
	Log *slog.Logger
}

func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

// scpShape matches the plain scp-style remote [user@]host:path, where the
// user part has no colon and so cannot hold a password.
var scpShape = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9.-]+:[^@\x00-\x1f]*$`)

const unparseableURL = "<unparseable repo URL>"

// RedactURL returns raw with credentials removed, so a repository URL is
// safe to log or to put in an error string. For every scheme except ssh, all
// userinfo goes (a token is often the username); for ssh only a password
// goes, as the user name (git) is not secret. A string that is not a
// parseable URL is returned unchanged only if it has the plain scp shape
// git@host:org/repo.git; anything else may hide a credential, so it becomes
// a fixed placeholder. Query strings and fragments are dropped too: a git
// remote needs neither, and a token can hide in either.
func RedactURL(raw string) string {
	// Local-looking paths (no scheme, no host, no userinfo) pass through
	// untouched, apart from the query/fragment cut.
	cut := raw
	if i := strings.IndexAny(cut, "?#"); i >= 0 {
		cut = cut[:i]
	}
	u, err := url.Parse(raw)
	// "user:tok@h:o/r.git" parses as scheme "user" with an opaque part, so
	// treat a host-less non-file URL like a parse failure.
	if err != nil || u.Opaque != "" || (u.Scheme != "" && u.Scheme != "file" && u.Host == "") {
		if scpShape.MatchString(cut) {
			return cut
		}
		return unparseableURL
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	if u.Scheme == "" && u.Host == "" && u.User == nil {
		return cut
	}
	if u.User != nil {
		if u.Scheme == "ssh" {
			if _, has := u.User.Password(); has {
				u.User = url.User(u.User.Username())
			}
		} else {
			u.User = nil
		}
	}
	return u.String()
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
// The reset does not touch untracked files, so anything gitignored dropped
// into the clone survives (decrypted secrets no longer live here -- see
// internal/sopsdecrypt -- but this stays as general protection for
// whatever else ends up untracked in the worktree).
func Sync(cfg Config, auth transport.AuthMethod) (Result, error) {
	l := logger(cfg.Log)
	repoURL := RedactURL(cfg.RepoURL)
	if _, statErr := os.Stat(cfg.ClonePath); errors.Is(statErr, os.ErrNotExist) {
		l.Info("clone path doesn't exist yet, cloning -- this is a first run, or someone tidied up", "path", cfg.ClonePath, "repo", repoURL, "branch", cfg.Branch)
		repo, err := git.PlainClone(cfg.ClonePath, false, &git.CloneOptions{
			URL:           cfg.RepoURL,
			Auth:          auth,
			ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
			SingleBranch:  true,
		})
		if err != nil {
			return Result{}, fmt.Errorf("cloning %s into %s: %w", repoURL, cfg.ClonePath, err)
		}
		head, err := repo.Head()
		if err != nil {
			return Result{}, fmt.Errorf("reading HEAD after cloning into %s: %w", cfg.ClonePath, err)
		}
		l.Info("clone complete", "commit", Short(head.Hash()), "path", cfg.ClonePath)
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
		return Result{}, fmt.Errorf("fetching %s in %s: %w", repoURL, cfg.ClonePath, err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", cfg.Branch), true)
	if err != nil {
		return Result{}, fmt.Errorf("resolving origin/%s in %s: %w", cfg.Branch, cfg.ClonePath, err)
	}

	// go-git's HardReset, unlike `git reset --hard`, deletes *every*
	// untracked file in the worktree, gitignored or not -- it diffs the
	// worktree against the index without applying gitignore rules. That
	// would silently wipe any gitignored file dropped into the clone on
	// every sync (originally written to guard decrypted secrets.env, back
	// when those lived here -- see internal/sopsdecrypt for where they live
	// now), so back up untracked files first and restore whichever ones the
	// reset removes. wt.Clean() must never be added after this: that's the
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

	if err := restoreMissingFiles(preserved, l); err != nil {
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
		l.Debug("synced, nothing new", "path", cfg.ClonePath, "commit", Short(res.After))
		return res, nil
	}
	l.Info("synced, new commits", "path", cfg.ClonePath, "previous", Short(res.Before), "commit", Short(res.After))
	return res, nil
}

// Short returns the first 12 hex characters of h, for log lines.
func Short(h plumbing.Hash) string { return h.String()[:12] }

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
func restoreMissingFiles(preserved map[string]preservedFile, l *slog.Logger) error {
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
		l.Info("restored untracked file after reset", "path", path)
	}
	return nil
}
