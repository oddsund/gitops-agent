package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	st := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if len(st) != 0 {
		t.Errorf("Load(missing) = %+v, want empty", st)
	}
}

func TestLoad_CorruptFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := Load(path)
	if len(st) != 0 {
		t.Errorf("Load(corrupt) = %+v, want empty", st)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deployed.json")
	want := State{"demoapp": "/opt/gitops/repo/services/demoapp"}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load(path)
	if len(got) != 1 || got["demoapp"] != want["demoapp"] {
		t.Errorf("Load after Save = %+v, want %+v", got, want)
	}
}

func TestSave_CreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "deployed.json")
	if err := Save(path, State{"thirdapp": "services/thirdapp"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not created: %v", err)
	}
}

func TestSave_OverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed.json")
	if err := Save(path, State{"a": "1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Save(path, State{"b": "2"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load(path)
	if len(got) != 1 || got["b"] != "2" {
		t.Errorf("Load after second Save = %+v, want only {b: 2}", got)
	}
}
