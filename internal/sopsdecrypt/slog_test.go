package sopsdecrypt

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecryptServiceSecrets_LogsNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	base := t.TempDir()
	enc, err := os.ReadFile("testdata/secrets.enc.env")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.enc.env"), enc, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := DecryptServiceSecrets(dir, "demoapp", "testdata/test_key", base, lg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `msg="wrote decrypted secrets"`) || !strings.Contains(out, "service=demoapp") {
		t.Errorf("log = %q", out)
	}
	for _, line := range strings.Split(wantPlaintext(t), "\n") {
		_, val, ok := strings.Cut(line, "=")
		if !ok || val == "" {
			continue
		}
		if strings.Contains(out, val) {
			t.Errorf("log contains plaintext value %q: %q", val, out)
		}
	}
}

func TestRemoveServiceSecrets_Logs(t *testing.T) {
	base := t.TempDir()
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, nil))

	if err := RemoveServiceSecrets("svc", base, lg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "svc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServiceSecrets("svc", base, lg); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("absent file or dir logged: %q", buf.String())
	}

	if err := os.MkdirAll(filepath.Join(base, "svc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "svc", "secrets.env"), []byte("A=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServiceSecrets("svc", base, lg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `msg="removed decrypted secrets"`) || !strings.Contains(out, "service=svc") {
		t.Errorf("log = %q", out)
	}
}
