package selfupdate

import (
	"fmt"
	"regexp"
)

var (
	// At most nine digits per component, so strconv.Atoi cannot overflow
	// even on 32-bit platforms.
	tagRE   = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	assetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// No leading '-': the name becomes a systemctl argument.
	serviceRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9:_.@-]*$`)
)

// These validators guard every value that is interpolated into a URL, an
// argv or a path. %q keeps control characters in the input escaped.

func validateTag(tag string) error {
	if !tagRE.MatchString(tag) {
		return fmt.Errorf("invalid release tag %q: want vMAJOR.MINOR.PATCH", tag)
	}
	return nil
}

func validateRepo(repo string) error {
	if !repoRE.MatchString(repo) {
		return fmt.Errorf("invalid repo %q: want OWNER/NAME", repo)
	}
	return nil
}

func validateAssetName(name string) error {
	if !assetRE.MatchString(name) {
		return fmt.Errorf("invalid asset name %q", name)
	}
	return nil
}

func validateServiceName(name string) error {
	if !serviceRE.MatchString(name) {
		return fmt.Errorf("invalid service name %q", name)
	}
	return nil
}
