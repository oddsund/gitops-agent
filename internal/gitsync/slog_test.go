package gitsync

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(g []string, a slog.Attr) slog.Attr {
			if len(g) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(h), &buf
}

func TestSync_LogsCloneWithRedactedRepo(t *testing.T) {
	remote := newRemote(t)
	lg, buf := newTestLogger()
	cfg := Config{RepoURL: remote, Branch: "main", ClonePath: filepath.Join(t.TempDir(), "clone"), Log: lg}
	if _, err := Sync(cfg, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `msg="clone complete"`) {
		t.Errorf("log lacks clone complete: %q", out)
	}
	if !regexp.MustCompile(`commit=[0-9a-f]{12}\b`).MatchString(out) {
		t.Errorf("log lacks a 12-char commit: %q", out)
	}
	if strings.Contains(out, "component=") {
		t.Errorf("gitsync must not stamp component: %q", out)
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://tok:secret@h/r.git", "https://h/r.git"},
		{"git@h:o/r.git", "git@h:o/r.git"},
		{"ssh://git@h/r.git", "ssh://git@h/r.git"},
		{"ssh://git:secret@h/r.git", "ssh://git@h/r.git"},
		{"https://ghp_TOKEN@h/r.git", "https://h/r.git"},
		{"https://u:p/ss@h/r.git", unparseableURL},
		{"https://u:p ss@h/r.git", unparseableURL},
		{"https://u:p%@h/r", unparseableURL},
		{"user:tok@h:o/r.git", unparseableURL},
		{"https://h/r.git?access_token=abc", "https://h/r.git"},
		{"https://h/r#token=abc", "https://h/r"},
		{"ssh://git@h/r?token=zz", "ssh://git@h/r"},
		{"git@h:o/r.git?token=abc", "git@h:o/r.git"},
		{"git@h:o/r.git\nx", unparseableURL},
		{`./a\x`, `./a\x`},
		{"/tmp/some/local/remote", "/tmp/some/local/remote"},
		{"file:///tmp/remote", "file:///tmp/remote"},
	}
	for _, c := range cases {
		got := RedactURL(c.in)
		if got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "TOKEN") || strings.Contains(got, "tok@") || strings.Contains(got, "token") || strings.Contains(got, "abc") {
			t.Errorf("RedactURL(%q) leaks the password: %q", c.in, got)
		}
	}
}
