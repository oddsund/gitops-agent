package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type writeResult struct {
	Changed bool
	// Created is true when the path did not exist before.
	Created bool
}

// writeAtomic makes path contain content with mode. Identical content and
// mode means no write, so the mtime stays untouched.
func writeAtomic(path, content string, mode os.FileMode) (writeResult, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return writeResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	old, err := os.ReadFile(path)
	existed := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return writeResult{}, fmt.Errorf("reading %s: %w", path, err)
	case existed && bytes.Equal(old, []byte(content)):
		fi, err := os.Stat(path)
		if err != nil {
			return writeResult{}, fmt.Errorf("writing %s: %w", path, err)
		}
		if fi.Mode().Perm() != mode {
			if err := os.Chmod(path, mode); err != nil {
				return writeResult{}, fmt.Errorf("writing %s: %w", path, err)
			}
		}
		return writeResult{}, nil
	}

	tmp, err := writeTemp(path, content, mode)
	if err != nil {
		return writeResult{}, err
	}
	// Same directory as path, so the rename is atomic.
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return writeResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return writeResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return writeResult{Changed: true, Created: !existed}, nil
}

// writeNew creates path only if it does not exist. The hard link makes the
// create atomic and no-clobber without a window where a half-written file
// is visible; unlike rename it cannot replace a file that appeared
// meanwhile.
func writeNew(path, content string, mode os.FileMode) (created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	tmp, err := writeTemp(path, content, mode)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// syncDir flushes a directory entry change, so a power loss (likely on the
// small boards this runs on) cannot lose a rename that already reported
// success.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeTemp writes content to a fresh temp file next to path and returns
// its name. The file is removed again on any error.
func writeTemp(path, content string, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".gitops-agent-install-*")
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return name, nil
}
