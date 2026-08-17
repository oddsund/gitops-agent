package gitsync

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// PathChanged reports whether the contents of path differ between the two
// commits. Path is relative to the repo root, e.g. "services/demoapp".
//
// This is what lets the agent skip redeploying services nothing touched.
// Git's tree objects are already content hashes of a whole directory, so
// this is a hash comparison, not a walk.
//
// A zero Before means "no previous state" (fresh clone), which counts as
// changed. So does a path that exists in only one of the two commits: a
// newly added service must deploy, and a removed one must be noticed.
func PathChanged(clonePath string, before, after plumbing.Hash, path string) (bool, error) {
	if before.IsZero() || before == after {
		return before.IsZero(), nil
	}

	repo, err := git.PlainOpen(clonePath)
	if err != nil {
		return false, fmt.Errorf("opening repo at %s: %w", clonePath, err)
	}

	beforeHash, err := subtreeHash(repo, before, path)
	if err != nil {
		return false, err
	}
	afterHash, err := subtreeHash(repo, after, path)
	if err != nil {
		return false, err
	}
	return beforeHash != afterHash, nil
}

// subtreeHash returns the tree hash of path in commit, or the zero hash if
// path doesn't exist there. A missing path is not an error: services get
// added and removed, and both sides of that are legitimate.
func subtreeHash(repo *git.Repository, commit plumbing.Hash, path string) (plumbing.Hash, error) {
	c, err := repo.CommitObject(commit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("reading commit %s: %w", short(commit), err)
	}
	tree, err := c.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("reading tree of %s: %w", short(commit), err)
	}
	sub, err := tree.Tree(path)
	if err != nil {
		if errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
			return plumbing.ZeroHash, nil
		}
		return plumbing.ZeroHash, fmt.Errorf("resolving %s in %s: %w", path, short(commit), err)
	}
	return sub.Hash, nil
}
