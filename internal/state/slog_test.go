package state

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_CorruptFileLogsWarn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	Load(path, slog.New(slog.NewTextHandler(&buf, nil)))
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, `msg="state file is corrupt, starting from empty state"`) {
		t.Errorf("log = %q", out)
	}
}
