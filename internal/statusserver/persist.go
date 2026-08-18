package statusserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultStatusFilePath is where the status snapshot is written after each
// cycle, matching the systemd unit's StateDirectory=gitops-agent (same
// directory as internal/state's deployed.json). It lets the last known
// status survive a crash or restart and be read without the HTTP server.
const DefaultStatusFilePath = "/var/lib/gitops-agent/status.json"

// WriteFile writes s to path as JSON. Like internal/state.Save, the write
// goes through a temp file plus rename so a crash mid-write never leaves
// path holding a half-written, corrupt file for the next read to stumble
// over.
func WriteFile(path string, s Status) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling status: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating status directory for %s: %w", path, err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
