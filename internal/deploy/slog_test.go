package deploy

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/oddsund/gitops-agent/internal/redact"
)

func stubDockerScript(t *testing.T, script string) string {
	t.Helper()
	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+":/usr/bin:/bin")
	return serviceDir
}

func TestDeploy_ErrorTextIsBounded(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\ni=0\nwhile [ $i -lt 300 ]; do echo 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; i=$((i+1)); done >&2\necho 'the last line' >&2\nexit 1\n")
	err := Deploy(dir, nil)
	if err == nil {
		t.Fatal("want error")
	}
	msg := strings.TrimSpace(redact.Detail(err))
	if len(msg) > 2300 {
		t.Errorf("error is %d bytes, want it bounded", len(msg))
	}
	if !strings.HasSuffix(msg, "the last line") {
		t.Errorf("error does not end with the stub's last line: %q", msg[max(0, len(msg)-60):])
	}
	if !strings.Contains(msg, "...(truncated)") {
		t.Error("error lacks the truncation marker")
	}
}

func TestDeploy_ErrorTextStaysValidUTF8(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\ni=0\nwhile [ $i -lt 2000 ]; do printf '\\303\\251\\303\\251\\303\\251\\303\\251\\303\\251' >&2; i=$((i+1)); done\nexit 1\n")
	err := Deploy(dir, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if !utf8.ValidString(redact.Detail(err)) {
		t.Error("error text is not valid UTF-8")
	}
}

func TestDeploy_SuccessOutputOnlyAtDebug(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\necho 'Container demoapp  Started' >&2\n")

	var dbg bytes.Buffer
	if err := Deploy(dir, slog.New(slog.NewTextHandler(&dbg, &slog.HandlerOptions{Level: slog.LevelDebug}))); err != nil {
		t.Fatal(err)
	}
	if out := dbg.String(); !strings.Contains(out, `msg="docker compose output"`) || !strings.Contains(out, "stream=stderr") {
		t.Errorf("debug log = %q", out)
	}

	var info bytes.Buffer
	if err := Deploy(dir, slog.New(slog.NewTextHandler(&info, nil))); err != nil {
		t.Fatal(err)
	}
	if info.Len() != 0 {
		t.Errorf("info log = %q, want empty", info.String())
	}
}

// deadlineCtx reports context.DeadlineExceeded once fire is called, so the
// test decides when "the timeout" happens instead of racing a timer.
type deadlineCtx struct {
	context.Context
	expired atomic.Bool
}

func (c *deadlineCtx) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

func TestRunCompose_TimeoutErrorTextIsBounded(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "written")
	dir := stubDockerScript(t, "#!/bin/sh\ni=0\nwhile [ $i -lt 300 ]; do echo 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; i=$((i+1)); done >&2\ntouch "+marker+"\nexec sleep 30\n")

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &deadlineCtx{Context: base}
	// Fire the "timeout" only after the stub has written all its output.
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				ctx.expired.Store(true)
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := runComposeCtx(ctx, dir, time.Minute, nil, "up")
	if err == nil {
		t.Fatal("want timeout error")
	}
	msg := redact.Detail(err)
	if !strings.Contains(msg, "timed out") {
		t.Fatalf("not the timeout branch: %q", msg)
	}
	if len(msg) > 2400 || !strings.Contains(msg, "...(truncated)") {
		t.Errorf("timeout error is %d bytes, truncated=%v", len(msg), strings.Contains(msg, "...(truncated)"))
	}
}
