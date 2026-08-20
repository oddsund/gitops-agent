package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// subcommands maps a subcommand name to its entry point. Empty for now --
// ga-tqa.2 (update) and ga-tqa.3 (install) register themselves here.
var subcommands = map[string]func(args []string) error{}

// isSubcommand reports whether args[0] should be dispatched as a
// subcommand rather than parsed as daemon flags. A bare invocation (no
// args) or a leading "-" (an actual flag, including -version) always
// means "run the daemon" -- gitops-agent.service's ExecStart and
// homelab's verify.bash both depend on that shape staying intact.
func isSubcommand(args []string) bool {
	return len(args) > 0 && !strings.HasPrefix(args[0], "-")
}

// dispatch runs the named subcommand and returns the process exit code.
// An unregistered name prints usage to stderr and returns non-zero,
// rather than falling through to daemon flag parsing.
func dispatch(stderr io.Writer, name string, args []string) int {
	cmd, ok := subcommands[name]
	if !ok {
		fmt.Fprintf(stderr, "gitops-agent: unknown subcommand %q\n\n", name)
		usage(stderr)
		return 1
	}
	if err := cmd(args); err != nil {
		fmt.Fprintf(stderr, "gitops-agent %s: %v\n", name, err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: gitops-agent [-config path] [-version]")

	names := make([]string, 0, len(subcommands))
	for name := range subcommands {
		names = append(names, name)
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)

	fmt.Fprintln(w, "       gitops-agent <subcommand> [args]")
	fmt.Fprintln(w, "\nsubcommands:")
	for _, name := range names {
		fmt.Fprintf(w, "  %s\n", name)
	}
}
