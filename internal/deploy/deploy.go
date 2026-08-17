// Package deploy applies a service's compose.yml via the docker CLI.
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds how long a single `docker compose up` invocation may
// run before it is killed. Generous enough for a cold image pull on a
// Raspberry Pi over a home connection, short enough that a wedged deploy
// self-clears within one or two reconcile cycles.
const DefaultTimeout = 10 * time.Minute

// Deploy runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans` for the given service directory, using DefaultTimeout.
func Deploy(serviceDir string) error {
	return DeployWithTimeout(serviceDir, DefaultTimeout)
}

// DeployWithTimeout runs `docker compose -f <serviceDir>/compose.yml up -d
// --remove-orphans` for the given service directory, killing the invocation
// if it does not complete within timeout.
func DeployWithTimeout(serviceDir string, timeout time.Duration) error {
	return runCompose(serviceDir, timeout, "up", "-d", "--remove-orphans")
}

// Down runs `docker compose -f <serviceDir>/compose.yml down
// --remove-orphans` for the given service directory, using DefaultTimeout.
// It's used to stop a service that's been disabled or removed from
// services.toml; serviceDir need not still be enabled in the manifest, but
// its compose.yml must still exist on disk (gitops-agent records the last
// known path for exactly this purpose -- see internal/state).
func Down(serviceDir string) error {
	return DownWithTimeout(serviceDir, DefaultTimeout)
}

// DownWithTimeout runs `docker compose -f <serviceDir>/compose.yml down
// --remove-orphans` for the given service directory, killing the invocation
// if it does not complete within timeout.
func DownWithTimeout(serviceDir string, timeout time.Duration) error {
	return runCompose(serviceDir, timeout, "down", "--remove-orphans")
}

// runCompose runs `docker compose -f <serviceDir>/compose.yml <composeArgs>`,
// killing the invocation if it does not complete within timeout.
func runCompose(serviceDir string, timeout time.Duration, composeArgs ...string) error {
	composeFile := filepath.Join(serviceDir, "compose.yml")
	args := append([]string{"compose", "-f", composeFile}, composeArgs...)
	log.Printf("deploy: running docker %s", strings.Join(args, " "))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = serviceDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("docker %s (%s): timed out after %s: %s", composeArgs[0], composeFile, timeout, stderr.String())
		}
		return fmt.Errorf("docker %s (%s): %w: %s", composeArgs[0], composeFile, err, stderr.String())
	}

	// docker compose writes progress to stderr even on success, so check
	// both streams.
	if out := strings.TrimSpace(stdout.String()); out != "" {
		log.Printf("deploy: docker compose stdout:\n%s", out)
	}
	if out := strings.TrimSpace(stderr.String()); out != "" {
		log.Printf("deploy: docker compose stderr:\n%s", out)
	}
	return nil
}
