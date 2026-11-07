// Package deploy applies a service's compose.yml via the docker CLI.
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

const maxOutput = 2048

// tail bounds compose output to its last 2 KiB, where compose puts the
// error. An env_file parse error can quote part of a decrypted value, so
// error text must never be unbounded.
func tail(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	i := len(s) - maxOutput
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "...(truncated) " + s[i:]
}

// DefaultTimeout bounds how long a single `docker compose up` invocation may
// run before it is killed. Generous enough for a cold image pull on a
// Raspberry Pi over a home connection, short enough that a wedged deploy
// self-clears within one or two reconcile cycles.
const DefaultTimeout = 10 * time.Minute

// Deploy runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans` for the given service directory, using DefaultTimeout.
func Deploy(serviceDir string, log *slog.Logger) error {
	return DeployWithTimeout(serviceDir, DefaultTimeout, log)
}

// DeployWithTimeout runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans` for the given service directory, killing the invocation
// if it does not complete within timeout.
func DeployWithTimeout(serviceDir string, timeout time.Duration, log *slog.Logger) error {
	return runCompose(serviceDir, timeout, log, "up", "-d", "--remove-orphans")
}

// Down runs `docker compose -f <serviceDir>/compose.yml down
// --remove-orphans` for the given service directory, using DefaultTimeout.
// It's used to stop a service that's been disabled or removed from
// services.toml; serviceDir need not still be enabled in the manifest, but
// its compose.yml must still exist on disk (gitops-agent records the last
// known path for exactly this purpose -- see internal/state).
func Down(serviceDir string, log *slog.Logger) error {
	return DownWithTimeout(serviceDir, DefaultTimeout, log)
}

// DownWithTimeout runs `docker compose -f <serviceDir>/compose.yml down
// --remove-orphans` for the given service directory, killing the invocation
// if it does not complete within timeout.
func DownWithTimeout(serviceDir string, timeout time.Duration, log *slog.Logger) error {
	return runCompose(serviceDir, timeout, log, "down", "--remove-orphans")
}

// runCompose runs `docker compose -f <serviceDir>/compose.yml <composeArgs>`,
// killing the invocation if it does not complete within timeout.
func runCompose(serviceDir string, timeout time.Duration, log *slog.Logger, composeArgs ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runComposeCtx(ctx, serviceDir, timeout, log, composeArgs...)
}

// runComposeCtx is runCompose with the deadline context supplied by the
// caller, so a test can trigger the timeout branch at a moment it controls
// instead of racing a wall-clock timer. timeout is only reported in logs
// and the error text.
func runComposeCtx(ctx context.Context, serviceDir string, timeout time.Duration, log *slog.Logger, composeArgs ...string) error {
	l := logger(log)
	composeFile := filepath.Join(serviceDir, "compose.yml")
	args := append([]string{"compose", "-f", composeFile}, composeArgs...)
	l.Debug("running docker compose", "path", serviceDir, "action", composeArgs[0], "timeout", timeout)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = serviceDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("docker %s (%s): timed out after %s: %s", composeArgs[0], composeFile, timeout, tail(stderr.String()))
		}
		return fmt.Errorf("docker %s (%s): %w: %s", composeArgs[0], composeFile, err, tail(stderr.String()))
	}

	// docker compose writes progress to stderr even on success, so check
	// both streams.
	if out := strings.TrimSpace(stdout.String()); out != "" {
		l.Debug("docker compose output", "path", serviceDir, "stream", "stdout", "output", tail(out))
	}
	if out := strings.TrimSpace(stderr.String()); out != "" {
		l.Debug("docker compose output", "path", serviceDir, "stream", "stderr", "output", tail(out))
	}
	return nil
}
