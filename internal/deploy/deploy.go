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

	"github.com/oddsund/gitops-agent/internal/redact"
)

func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

const maxOutput = 2048

// tail bounds compose output to its last 2 KiB, where compose puts the
// error. It only bounds size: the caller must scrub secret values first,
// because a cut can split a value in half and the half no longer matches.
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

// Runner runs docker compose for a service directory. SecretsDir is the
// base directory of the decrypted env files (<SecretsDir>/<service>/secrets.env);
// compose output is scrubbed of the values in them before it is stored in an
// error or logged. An empty SecretsDir scrubs nothing and is for tests only.
type Runner struct {
	Timeout    time.Duration // zero means DefaultTimeout
	Log        *slog.Logger
	SecretsDir string
}

func (r Runner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

// Deploy runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans`. A failure is a *redact.Error: its message is a fixed
// summary and the scrubbed compose output is in its Detail.
func (r Runner) Deploy(serviceDir string) error {
	return r.runWithTimeout(serviceDir, "up", "-d", "--remove-orphans")
}

// Down runs `docker compose -f <serviceDir>/compose.yml down
// --remove-orphans`. It's used to stop a service that's been disabled or
// removed from services.toml; its compose.yml must still exist on disk
// (gitops-agent records the last known path for exactly this purpose -- see
// internal/state). Errors are as for Deploy.
func (r Runner) Down(serviceDir string) error {
	return r.runWithTimeout(serviceDir, "down", "--remove-orphans")
}

func (r Runner) runWithTimeout(serviceDir string, composeArgs ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout())
	defer cancel()
	return r.run(ctx, serviceDir, composeArgs...)
}

// Deploy runs `docker compose up` using DefaultTimeout. It does not scrub;
// production code uses Runner.
func Deploy(serviceDir string, log *slog.Logger) error {
	return DeployWithTimeout(serviceDir, DefaultTimeout, log)
}

// DeployWithTimeout is Deploy with a custom timeout; a non-positive timeout
// means DefaultTimeout. It does not scrub; production code uses Runner.
func DeployWithTimeout(serviceDir string, timeout time.Duration, log *slog.Logger) error {
	return Runner{Timeout: timeout, Log: log}.Deploy(serviceDir)
}

// Down runs `docker compose down` using DefaultTimeout. It does not scrub;
// production code uses Runner.
func Down(serviceDir string, log *slog.Logger) error {
	return DownWithTimeout(serviceDir, DefaultTimeout, log)
}

// DownWithTimeout is Down with a custom timeout; a non-positive timeout
// means DefaultTimeout. It does not scrub; production code uses Runner.
func DownWithTimeout(serviceDir string, timeout time.Duration, log *slog.Logger) error {
	return Runner{Timeout: timeout, Log: log}.Down(serviceDir)
}

// runComposeCtx runs compose with the deadline context supplied by the
// caller, so a test can trigger the timeout branch at a moment it controls
// instead of racing a wall-clock timer. timeout is only reported in logs
// and the error text.
func runComposeCtx(ctx context.Context, serviceDir string, timeout time.Duration, log *slog.Logger, composeArgs ...string) error {
	return Runner{Timeout: timeout, Log: log}.run(ctx, serviceDir, composeArgs...)
}

// summary is the text that may be shown on the status page, in /healthz and
// in email. It must never contain compose output.
func summary(ctx context.Context, action string, timeout time.Duration, runErr error) string {
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("docker compose %s timed out after %s; see the journal", action, timeout)
	case errors.As(runErr, &exit) && exit.ExitCode() >= 0:
		return fmt.Sprintf("docker compose %s failed with exit code %d; see the journal", action, exit.ExitCode())
	case errors.As(runErr, &exit):
		return fmt.Sprintf("docker compose %s was killed by a signal; see the journal", action)
	default:
		return fmt.Sprintf("docker compose %s could not run; see the journal", action)
	}
}

func (r Runner) run(ctx context.Context, serviceDir string, composeArgs ...string) error {
	l := logger(r.Log)
	timeout := r.timeout()
	composeFile := filepath.Join(serviceDir, "compose.yml")
	args := append([]string{"compose", "-f", composeFile}, composeArgs...)
	action := composeArgs[0]
	l.Debug("running docker compose", "path", serviceDir, "action", action, "timeout", timeout)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = serviceDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		// Scrub BEFORE tail: cutting first could split a value in half and
		// the half would no longer match.
		sc := redact.LoadEnvDir(r.SecretsDir)
		detail := tail(sc.Scrub(stderr.String()))
		sum := summary(ctx, action, timeout, runErr)
		cause := runErr.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cause = "timed out after " + timeout.String()
		}
		return &redact.Error{
			Summary: sum,
			Detail:  composeFile + ": " + cause + ": " + detail,
		}
	}

	// docker compose writes progress to stderr even on success, so check
	// both streams. Output is only built into a log line at debug level, so
	// the secrets are only loaded then.
	so, se := strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())
	if (so != "" || se != "") && l.Enabled(ctx, slog.LevelDebug) {
		sc := redact.LoadEnvDir(r.SecretsDir)
		if so != "" {
			l.Debug("docker compose output", "path", serviceDir, "stream", "stdout", "output", tail(sc.Scrub(so)))
		}
		if se != "" {
			l.Debug("docker compose output", "path", serviceDir, "stream", "stderr", "output", tail(sc.Scrub(se)))
		}
	}
	return nil
}
