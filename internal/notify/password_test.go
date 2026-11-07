package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePW(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPassword(t *testing.T) {
	big := strings.Repeat("x", 5*1024)
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		want    string
		wantErr string
	}{
		{"ok with newline", "s3cret\n", 0o600, "s3cret", ""},
		{"ok crlf", "s3cret\r\n", 0o600, "s3cret", ""},
		{"read-only owner", "s3cret", 0o400, "s3cret", ""},
		{"group readable", "s3cret", 0o640, "", "accessible to group or others"},
		{"world readable", "s3cret", 0o604, "", "accessible to group or others"},
		{"empty", "", 0o600, "", "one non-empty line"},
		{"only newline", "\n", 0o600, "", "one non-empty line"},
		{"two lines", "a\nb\n", 0o600, "", "one non-empty line"},
		{"nul", "a\x00b", 0o600, "", "one non-empty line"},
		{"too big", big, 0o600, "", "larger than"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writePW(t, tc.content, tc.mode)
			got, err := LoadPassword(p)
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error lacks path: %v", err)
			}
			if tc.content != "" && strings.Contains(err.Error(), strings.TrimSpace(tc.content)) && len(tc.content) > 2 {
				t.Errorf("error contains file content: %v", err)
			}
		})
	}
}

func TestLoadPassword_DirectoryAndMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPassword(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: %v", err)
	}
	missing := filepath.Join(dir, "nope")
	if _, err := LoadPassword(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing: %v", err)
	}
}

func TestLoadPassword_SymlinkChecksTarget(t *testing.T) {
	target := writePW(t, "pw\n", 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if got, err := LoadPassword(link); err != nil || got != "pw" {
		t.Errorf("got %q, %v", got, err)
	}
	open := writePW(t, "pw\n", 0o644)
	link2 := filepath.Join(t.TempDir(), "link2")
	if err := os.Symlink(open, link2); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := LoadPassword(link2); err == nil {
		t.Error("symlink to a world-readable file accepted")
	}
}

func TestLoadPassword_OwnerMustBeAgentUID(t *testing.T) {
	p := writePW(t, "pw\n", 0o600)
	if _, err := loadPassword(p, func() int { return os.Geteuid() + 1 }); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("foreign owner accepted: %v", err)
	}
	if got, err := loadPassword(p, os.Geteuid); err != nil || got != "pw" {
		t.Errorf("own file: %q, %v", got, err)
	}
}
