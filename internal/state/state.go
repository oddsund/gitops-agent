// Package state persists which services gitops-agent last successfully
// deployed, and where, so a service can be torn down even after its
// [[services]] block disappears from services.toml -- at that point the
// manifest no longer has the path, and this is the only place left to find
// it.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// State maps a service's name (services.toml's [[services]].name) to the
// service directory gitops-agent last deployed it from.
type State map[string]string

// Load reads the state file at path. A missing or corrupt file is not an
// error: it's the normal case on a fresh install, and refusing to start
// over it would make a single bad write brick the reconcile loop for
// everything, not just teardown.
func Load(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("state: %s unreadable (%v), starting from empty state", path, err)
		}
		return State{}
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("state: %s is corrupt (%v), starting from empty state", path, err)
		return State{}
	}
	if st == nil {
		st = State{}
	}
	return st
}

// Save writes st to path, creating its parent directory if needed. The
// write goes through a temp file plus rename so a crash mid-write can never
// leave path holding a half-written, corrupt file for the next Load to
// stumble over.
func Save(path string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating state directory for %s: %w", path, err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
