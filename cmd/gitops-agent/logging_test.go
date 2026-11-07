package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, slog.LevelInfo, false).Info("hello")
	if !strings.Contains(buf.String(), "time=") {
		t.Errorf("non-journal output lacks time=: %q", buf.String())
	}

	buf.Reset()
	newLogger(&buf, slog.LevelInfo, true).Info("hello")
	if strings.Contains(buf.String(), "time=") || !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("journal output = %q", buf.String())
	}

	buf.Reset()
	lg := newLogger(&buf, slog.LevelInfo, true)
	lg.Debug("hidden")
	if buf.Len() != 0 {
		t.Errorf("debug shown at info: %q", buf.String())
	}
	lg.Error("boom")
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := parseLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseLevel("bogus"); err == nil {
		t.Error("parseLevel(bogus) = nil error")
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("GA_TEST_ENVOR", "")
	if got := envOr("GA_TEST_ENVOR", "def"); got != "def" {
		t.Errorf("got %q", got)
	}
	t.Setenv("GA_TEST_ENVOR", "x")
	if got := envOr("GA_TEST_ENVOR", "def"); got != "x" {
		t.Errorf("got %q", got)
	}
}
