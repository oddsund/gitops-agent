package gitsync

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain makes every git process started by this package's tests ignore
// the host: the developer's git configuration and any inherited GIT_*
// variables. A global commit.gpgsign=true with a locked signing agent (for
// example 1Password SSH) makes `git commit` hang or fail, and `go test` run
// from a git hook inherits GIT_DIR and GIT_INDEX_FILE, which point git at
// the wrong repository. go-git's local-path transport shells out to
// git-upload-pack, which reads the same environment, so the setup is
// process-wide rather than per command.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gitsync-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp home:", err)
		os.Exit(1)
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
		}
	}
	env := map[string]string{
		// HOME and XDG_CONFIG_HOME are the fallback for git < 2.32, which
		// ignores GIT_CONFIG_GLOBAL.
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_NAME":     "test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
		// Forces unsigned commits on git >= 2.31 even if some config source
		// is not covered by the settings above.
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "commit.gpgsign",
		"GIT_CONFIG_VALUE_0": "false",
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "setenv:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
