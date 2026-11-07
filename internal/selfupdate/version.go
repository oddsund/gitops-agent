package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// parseVersion accepts only plain vMAJOR.MINOR.PATCH tags; pre-release and
// build suffixes are rejected rather than ordered.
func parseVersion(s string) ([3]int, error) {
	var v [3]int
	if err := validateTag(s); err != nil {
		return v, err
	}
	for i, part := range strings.Split(strings.TrimPrefix(s, "v"), ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, fmt.Errorf("invalid release tag %q: %w", s, err)
		}
		v[i] = n
	}
	return v, nil
}

// compareVersions follows cmp.Compare(installed, latest): negative means the
// installed version is older.
func compareVersions(installed, latest string) (int, error) {
	a, err := parseVersion(installed)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(latest)
	if err != nil {
		return 0, err
	}
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}
