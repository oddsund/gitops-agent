package statusserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "status.json")

	want := Status{Version: "v1.2.3", StartedAt: time.Now().Truncate(time.Second)}
	if err := WriteFile(path, want); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back %s: %v", path, err)
	}
	var got Status
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %q, want %q", got.Version, want.Version)
	}

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file was left behind after a successful write")
	}
}

func TestWriteFile_Overwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")

	if err := WriteFile(path, Status{Version: "v1"}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteFile(path, Status{Version: "v2"}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var got Status
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got.Version != "v2" {
		t.Errorf("Version = %q, want v2", got.Version)
	}
}
