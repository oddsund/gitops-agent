// Package cmdrun is the one place external commands are started, so callers
// can be tested with a fake.
package cmdrun

import (
	"context"
	"os"
	"os/exec"
	"time"
)

const waitDelay = 5 * time.Second

// Runner starts an external command and returns its combined stdout and
// stderr. env entries are KEY=value and are added to the process
// environment.
type Runner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// Real runs commands on the host. It never involves a shell, so arguments
// are not subject to expansion or splitting.
type Real struct{}

// Run returns the raw start or exit error; callers add their own context.
func (Real) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Later entries win, so env overrides inherited variables.
	cmd.Env = append(os.Environ(), env...)
	// Without a delay, a grandchild that inherits the output pipe keeps
	// CombinedOutput blocked after the context has killed the command.
	cmd.WaitDelay = waitDelay
	return cmd.CombinedOutput()
}
