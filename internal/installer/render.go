package installer

import (
	"fmt"
	"strings"
)

type lineSub struct{ Prefix, Value string }

// substituteLines replaces every line of tmpl that starts with a sub's
// Prefix by Prefix+Value. Each sub must match exactly one line, so a
// template that drifts fails loudly instead of yielding a half-substituted
// file. Matching happens before any output is produced. All other lines
// pass through byte for byte, including the final newline state.
func substituteLines(tmpl string, subs []lineSub) (string, error) {
	lines := strings.Split(tmpl, "\n")
	for _, s := range subs {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, s.Prefix) {
				n++
			}
		}
		if n != 1 {
			return "", fmt.Errorf("template has %d lines starting with %q, want exactly 1", n, s.Prefix)
		}
		if strings.ContainsAny(s.Value, "\r\n") {
			return "", fmt.Errorf("%s value contains a line break", s.Prefix)
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l
		for _, s := range subs {
			if strings.HasPrefix(l, s.Prefix) {
				out[i] = s.Prefix + s.Value
				break
			}
		}
	}
	return strings.Join(out, "\n"), nil
}

func renderServiceUnit(tmpl, user, readWritePaths string) (string, error) {
	return substituteLines(tmpl, []lineSub{
		{"User=", user},
		{"ReadWritePaths=", readWritePaths},
	})
}

func renderConfigTOML(tmpl, repoURL, sshKeyPath, clonePath string) (string, error) {
	return substituteLines(tmpl, []lineSub{
		{"repo_url = ", tomlQuote(repoURL)},
		{"ssh_key_path = ", tomlQuote(sshKeyPath)},
		{"clone_path = ", tomlQuote(clonePath)},
	})
}

// tomlQuote makes a TOML basic string. Precondition: s has no control
// characters (the validators guarantee it). %q is not used because Go emits
// \x.. , \a and \v escapes that are invalid TOML.
func tomlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
