package installer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteAtomic_CreatesWithMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	res, err := writeAtomic(path, "hello", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.Created {
		t.Errorf("result = %+v, want Changed and Created", res)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteAtomic_ReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := writeAtomic(path, "new", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Created {
		t.Errorf("result = %+v, want Changed and not Created", res)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("content = %q", b)
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != "f" {
		t.Errorf("dir = %v, want only f", names)
	}
}

func TestWriteAtomic_IdenticalIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if _, err := writeAtomic(path, "x", 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	res, err := writeAtomic(path, "x", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("identical content reported as changed")
	}
	fi, _ := os.Stat(path)
	if fi.ModTime().Sub(past).Abs() > time.Second {
		t.Errorf("mtime changed to %v", fi.ModTime())
	}
}

func TestWriteAtomic_FixesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := writeAtomic(path, "x", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("mode-only fix reported as changed")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteAtomic_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "f")
	if _, err := writeAtomic(path, "x", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}
}

func TestWriteAtomic_FailureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAtomic(target, "x", 0o644); err == nil {
		t.Fatal("expected an error when the target is a non-empty directory")
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != "target" {
		t.Errorf("dir = %v, want only target", names)
	}
}

func TestWriteNew_Creates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	created, err := writeNew(path, "x", 0o644)
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("dir = %v, want only f", names)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestWriteNew_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := writeNew(path, "other", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true for an existing file")
	}
	if b, _ := os.ReadFile(path); string(b) != "KEEP" {
		t.Errorf("content = %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("dir = %v, want only f", names)
	}
}
