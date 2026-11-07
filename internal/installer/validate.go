package installer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"unicode"
	"unicode/utf8"
)

var (
	userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	// ReadWritePaths= is whitespace-separated and systemd expands %
	// specifiers, hence the narrow allow-list: no space, %, $, ", \ or :.
	// ".." is excluded by the Clean comparison. A leading "-" cannot occur
	// because the path must start with "/".
	unitPathRE = regexp.MustCompile(`^/[A-Za-z0-9._+@/-]+$`)
	// Same grammar as the release tags selfupdate accepts (vX.Y.Z, no
	// pre-release), kept private so the packages stay independent.
	versionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

const maxTextLen = 4096

// validateUser refuses root because the agent service must not run as root.
func validateUser(u string) error {
	if !userRE.MatchString(u) {
		return fmt.Errorf("invalid user %q: must match %s", u, userRE)
	}
	if u == "root" {
		return fmt.Errorf("invalid user %q: the agent service must not run as root", u)
	}
	return nil
}

// validateUnitPath is for values that end up in a unit file.
func validateUnitPath(field, p string) error {
	switch {
	case len(p) > maxTextLen:
		return fmt.Errorf("invalid %s %q: longer than %d bytes", field, p, maxTextLen)
	case !unitPathRE.MatchString(p):
		return fmt.Errorf("invalid %s %q: must be an absolute path of letters, digits and . _ + @ / -", field, p)
	case filepath.Clean(p) != p || p == "/":
		return fmt.Errorf("invalid %s %q: must be clean and not the root directory", field, p)
	}
	return nil
}

// validateSSHKeyPath is for values that end up in TOML only.
func validateSSHKeyPath(p string) error {
	if err := validTOMLText("ssh key path", p); err != nil {
		return err
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("invalid ssh key path %q: must be a clean absolute path", p)
	}
	return nil
}

func validateRepoURL(u string) error {
	if err := validTOMLText("repo URL", u); err != nil {
		return err
	}
	if len(u) > 2048 {
		return fmt.Errorf("invalid repo URL %q: longer than 2048 bytes", u)
	}
	if u[0] == '-' {
		return fmt.Errorf("invalid repo URL %q: must not start with '-'", u)
	}
	for _, r := range u {
		if unicode.IsSpace(r) {
			return fmt.Errorf("invalid repo URL %q: must not contain whitespace", u)
		}
	}
	return nil
}

func validateRoot(r string) error {
	if r != "" && !filepath.IsAbs(r) {
		return fmt.Errorf("invalid root %q: must be absolute", r)
	}
	return nil
}

// validTOMLText rejects control characters instead of escaping them, so
// tomlQuote only ever has to deal with backslash and double quote.
func validTOMLText(field, s string) error {
	switch {
	case s == "":
		return fmt.Errorf("invalid %s %q: empty", field, s)
	case len(s) > maxTextLen:
		return fmt.Errorf("invalid %s %q: longer than %d bytes", field, s, maxTextLen)
	case !utf8.ValidString(s):
		return fmt.Errorf("invalid %s %q: not valid UTF-8", field, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid %s %q: contains a control character", field, s)
		}
	}
	return nil
}
