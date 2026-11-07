package selfupdate

import (
	"strings"
	"testing"
)

func checkValidator(t *testing.T, name string, fn func(string) error, valid, invalid []string) {
	t.Helper()
	for _, s := range valid {
		if err := fn(s); err != nil {
			t.Errorf("%s(%q) = %v, want nil", name, s, err)
		}
	}
	for _, s := range invalid {
		if err := fn(s); err == nil {
			t.Errorf("%s(%q) = nil, want error", name, s)
		}
	}
}

func TestValidateTag(t *testing.T) {
	checkValidator(t, "validateTag", validateTag,
		[]string{"v0.0.1", "v10.20.30", "v999999999.999999999.999999999"},
		[]string{"", "testVersion", "V1.2.3", "v1.2", "v1.2.3-rc1", "v1.2.3\n", "v1.2.3 ",
			"v1.2.3/../x", "v1.2.3%2f", "v1.2.3?x=1", "v1.2.3#f",
			"v1.2." + strings.Repeat("9", 40), "v99999999999999999999.0.0", "v1234567890.0.0"})
}

func TestValidateRepo(t *testing.T) {
	checkValidator(t, "validateRepo", validateRepo,
		[]string{"a/b", "oddsund/gitops-agent"},
		[]string{"", "a", "a/b/c", "../x", "a/..", "-x/y", "a b/c", "a/b\n"})
}

func TestValidateServiceName(t *testing.T) {
	checkValidator(t, "validateServiceName", validateServiceName,
		[]string{"gitops-agent", "foo@bar.service"},
		[]string{"--now", "-x", "", "a b"})
}

func TestValidateAssetName(t *testing.T) {
	checkValidator(t, "validateAssetName", validateAssetName,
		[]string{"gitops-agent-linux-arm64", "test-asset"},
		[]string{"", "../x", "a/b", "-x", "a b", "a\n"})
}
