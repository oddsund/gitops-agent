package deploy

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oddsund/gitops-agent/internal/redact"
)

const canary = "s3cr3t-Canary-9f2a1b"

// secretsFixture writes <tmp>/secrets/demo/secrets.env with the given
// content and returns the secrets base dir.
func secretsFixture(t *testing.T, content string) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(filepath.Join(base, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "demo", "secrets.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return base
}

func debugLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// stubStderr returns a service dir whose fake docker prints the content of
// a file to stderr and exits with code.
func stubStderr(t *testing.T, stderr string, code int) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "stderr.txt")
	if err := os.WriteFile(f, []byte(stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	return stubDockerScript(t, fmt.Sprintf("#!/bin/sh\ncat %s >&2\nexit %d\n", f, code))
}

func TestRunner_DeployErrorHidesStderr(t *testing.T) {
	dir := stubStderr(t, `error parsing env_file: line 1: unexpected "API_KEY=`+canary+`"`+"\n", 1)
	lg, buf := debugLog()
	err := Runner{Log: lg, SecretsDir: secretsFixture(t, "API_KEY="+canary+"\n")}.Deploy(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if want := "docker compose up failed with exit code 1; see the journal"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	d := redact.Detail(err)
	if !strings.Contains(d, "***") || strings.Contains(d, canary) {
		t.Errorf("detail = %q", d)
	}
	if strings.Contains(buf.String(), canary) {
		t.Errorf("log leaks the canary: %s", buf.String())
	}
}

func TestRunner_DownErrorHidesStderr(t *testing.T) {
	dir := stubStderr(t, `unexpected "API_KEY=`+canary+`"`+"\n", 3)
	err := Runner{SecretsDir: secretsFixture(t, "API_KEY="+canary+"\n")}.Down(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if want := "docker compose down failed with exit code 3; see the journal"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if d := redact.Detail(err); !strings.Contains(d, "***") || strings.Contains(d, canary) {
		t.Errorf("detail = %q", d)
	}
}

// TestRunner_QuotedForms pins each candidate form from redact.EnvValues:
// dropping one lets the matching case through.
func TestRunner_QuotedForms(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		printed string
	}{
		{"double quoted", `K="` + canary + `"`, `K="` + canary + `"`},
		{"single quoted", `K='` + canary + `'`, `K='` + canary + `'`},
		{"inline comment", `K=` + canary + ` # note`, canary},
		{"export", `export K=` + canary, canary},
		{"escaped newline", `K="` + canary + `\n` + canary + `"`, canary + "\n" + canary},
		{"go quoted", `K=` + canary + `"x"`, canary + `\"x\"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubStderr(t, "boom: "+tc.printed+"\n", 1)
			err := Runner{SecretsDir: secretsFixture(t, tc.env+"\n")}.Deploy(dir)
			if err == nil {
				t.Fatal("want error")
			}
			if d := redact.Detail(err); strings.Contains(d, canary) {
				t.Errorf("detail leaks the canary: %q", d)
			}
		})
	}
}

// TestRunner_ScrubsBeforeTruncating pins the order: the 2 KiB window starts
// 8 bytes into the canary. Truncating first would leave a fragment that no
// longer matches the value.
func TestRunner_ScrubsBeforeTruncating(t *testing.T) {
	stderr := strings.Repeat("x", 5000) + canary + strings.Repeat("y", maxOutput-(len(canary)-8))
	dir := stubStderr(t, stderr, 1)
	err := Runner{SecretsDir: secretsFixture(t, "K="+canary+"\n")}.Deploy(dir)
	if err == nil {
		t.Fatal("want error")
	}
	d := redact.Detail(err)
	if !strings.Contains(d, "...(truncated)") {
		t.Fatalf("test setup: nothing was truncated: %d bytes", len(d))
	}
	if strings.Contains(d, canary[8:]) || strings.Contains(d, canary[:8]) {
		t.Errorf("detail holds a canary fragment")
	}
}

func TestRunner_FragmentNotScrubbedButSummaryIsSafe(t *testing.T) {
	dir := stubStderr(t, "near: "+canary[:10]+"\n", 1)
	err := Runner{SecretsDir: secretsFixture(t, "K="+canary+"\n")}.Deploy(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), canary[:10]) {
		t.Errorf("summary leaks a fragment: %q", err.Error())
	}
}

func TestRunner_TimeoutSummary(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\necho '"+canary+"' >&2\nexec sleep 30\n")
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &deadlineCtx{Context: base}
	ctx.expired.Store(true)
	cancel()
	err := Runner{Timeout: 10 * time.Minute, SecretsDir: secretsFixture(t, "K="+canary+"\n")}.run(ctx, dir, "up")
	if err == nil {
		t.Fatal("want error")
	}
	if want := "docker compose up timed out after 10m0s; see the journal"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRunner_CouldNotRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")
	err := Runner{}.Deploy(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if want := "docker compose up could not run; see the journal"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRunner_KilledBySignal(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\nkill -9 $$\n")
	err := Runner{}.Deploy(dir)
	if err == nil {
		t.Fatal("want error")
	}
	if want := "docker compose up was killed by a signal; see the journal"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRunner_SuccessOutputScrubbedInDebugLog(t *testing.T) {
	dir := stubDockerScript(t, "#!/bin/sh\necho 'using "+canary+"' >&2\necho 'out "+canary+"'\n")
	lg, buf := debugLog()
	if err := (Runner{Log: lg, SecretsDir: secretsFixture(t, "K="+canary+"\n")}).Deploy(dir); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "***") || strings.Contains(out, canary) {
		t.Errorf("debug log = %s", out)
	}
}

func TestRunner_ErrorNeverHoldsStderr(t *testing.T) {
	dir := stubStderr(t, "unscrubbed-marker-text\n", 1)
	err := Runner{}.Deploy(dir)
	if err == nil || strings.Contains(err.Error(), "unscrubbed-marker-text") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(redact.Detail(err), "unscrubbed-marker-text") {
		t.Error("detail lost the compose output")
	}
}
