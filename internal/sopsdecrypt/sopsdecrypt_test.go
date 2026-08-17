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

	if err := DecryptServiceSecrets(dir, "demoapp", "testdata/test_key", secretsBaseDir); err != nil {
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

	if err := DecryptServiceSecrets(dir, "demoapp", "testdata/test_key", secretsBaseDir); err != nil {
		t.Fatalf("expected no error when secrets.enc.env is absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(secretsBaseDir, "demoapp", "secrets.env")); !os.IsNotExist(err) {
		t.Error("expected no secrets.env to be written")
	}
}
