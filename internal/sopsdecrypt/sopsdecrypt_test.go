package sopsdecrypt

import (
	"os"
	"path/filepath"
	"testing"
)

func wantPlaintext(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/decrypted.env")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDecryptFile_SSHNativeAgeIdentity is the empirical check that the sops
// Go library (not just the sops CLI) actually picks up an SSH ed25519 key
// as a native age identity, with no ssh-to-age conversion step.
func TestDecryptFile_SSHNativeAgeIdentity(t *testing.T) {
	out, err := DecryptFile("testdata/secrets.enc.env", "testdata/test_key")
	if err != nil {
		t.Fatalf("DecryptFile: %v", err)
	}
	if want := wantPlaintext(t); string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestDecryptServiceSecrets(t *testing.T) {
	dir := t.TempDir()
	secretsBaseDir := t.TempDir()
	encContent, err := os.ReadFile("testdata/secrets.enc.env")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), encContent, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := DecryptServiceSecrets(dir, "demoapp", "testdata/test_key", secretsBaseDir, nil); err != nil {
		t.Fatalf("DecryptServiceSecrets: %v", err)
	}

	outPath := filepath.Join(secretsBaseDir, "demoapp", "secrets.env")
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := wantPlaintext(t); string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Never written back into the service directory (the git worktree).
	if _, err := os.Stat(filepath.Join(dir, "secrets.env")); !os.IsNotExist(err) {
		t.Error("secrets.env was written into serviceDir, want it only under secretsBaseDir")
	}
}

func TestDecryptServiceSecrets_NoEncFile(t *testing.T) {
	dir := t.TempDir()
	secretsBaseDir := t.TempDir()

	if err := DecryptServiceSecrets(dir, "demoapp", "testdata/test_key", secretsBaseDir, nil); err != nil {
		t.Fatalf("expected no error when secrets.enc.env is absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(secretsBaseDir, "demoapp", "secrets.env")); !os.IsNotExist(err) {
		t.Error("expected no secrets.env to be written")
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestRemoveServiceSecrets_RemovesFileAndDir(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "svc", "secrets.env"))

	if err := RemoveServiceSecrets("svc", base, nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(base, "svc")) {
		t.Error("service dir still present")
	}
	if !exists(base) {
		t.Error("base dir was removed")
	}
}

func TestRemoveServiceSecrets_NotExistIsSuccess(t *testing.T) {
	base := t.TempDir()
	if err := RemoveServiceSecrets("svc", base, nil); err != nil {
		t.Fatalf("no dir: %v", err)
	}

	if err := os.Mkdir(filepath.Join(base, "svc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServiceSecrets("svc", base, nil); err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if exists(filepath.Join(base, "svc")) {
		t.Error("empty leftover dir not removed")
	}
}

func TestRemoveServiceSecrets_KeepsDirWithOtherFile(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "svc", "secrets.env"))
	writeFile(t, filepath.Join(base, "svc", "other"))

	if err := RemoveServiceSecrets("svc", base, nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(base, "svc", "secrets.env")) {
		t.Error("secrets.env still present")
	}
	if !exists(filepath.Join(base, "svc", "other")) {
		t.Error("other file was deleted")
	}
}

func TestRemoveServiceSecrets_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "run")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	canaries := []string{
		filepath.Join(root, "canary", "secrets.env"),
		filepath.Join(root, "secrets.env"),
	}
	for _, c := range canaries {
		writeFile(t, c)
	}

	for _, name := range []string{"../canary", "..", ".", "", "a/b", "/abs", `a\b`, "a\x00b"} {
		if err := RemoveServiceSecrets(name, base, nil); err == nil {
			t.Errorf("name %q: want error, got nil", name)
		}
	}
	if err := RemoveServiceSecrets("svc", "", nil); err == nil {
		t.Error("empty base: want error, got nil")
	}
	for _, c := range canaries {
		if !exists(c) {
			t.Errorf("canary %s was deleted", c)
		}
	}
}

func TestRemoveServiceSecrets_OtherServiceUntouched(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "one", "secrets.env"))
	writeFile(t, filepath.Join(base, "two", "secrets.env"))

	if err := RemoveServiceSecrets("one", base, nil); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(base, "two", "secrets.env")) {
		t.Error("other service's secrets were deleted")
	}
}

func TestRemoveServiceSecrets_FileRemovalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "svc")
	writeFile(t, filepath.Join(dir, "secrets.env"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := RemoveServiceSecrets("svc", base, nil); err == nil {
		t.Fatal("want error, got nil")
	}
	if !exists(dir) {
		t.Error("dir removed despite file removal failure")
	}
}

func TestRemoveServiceSecrets_RefusesSymlinkedServiceDir(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "run")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(outside, "secrets.env")
	writeFile(t, canary)
	link := filepath.Join(base, "svc")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if err := RemoveServiceSecrets("svc", base, nil); err == nil {
		t.Fatal("want error, got nil")
	}
	if !exists(canary) {
		t.Error("canary behind symlink was deleted")
	}
	if !exists(link) {
		t.Error("symlink was removed")
	}
}
