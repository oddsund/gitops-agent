package installer

import (
	"strings"
	"testing"
)

type validateCase struct {
	in      string
	wantErr bool
}

func runValidateCases(t *testing.T, fn func(string) error, cases []validateCase) {
	t.Helper()
	for _, tc := range cases {
		name := tc.in
		if len(name) > 40 {
			name = name[:40] + "..."
		}
		t.Run(name, func(t *testing.T) {
			err := fn(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateUser(t *testing.T) {
	runValidateCases(t, validateUser, []validateCase{
		{"pi", false}, {"gitops", false}, {"_svc", false}, {"a-b_c", false},
		{strings.Repeat("a", 32), false},
		{"", true}, {"root", true}, {"Pi", true}, {"1pi", true}, {"-pi", true},
		{strings.Repeat("a", 33), true},
		{"pi\nExecStartPre=/bin/x", true}, {"pi x", true}, {"pi%h", true}, {"pi;x", true},
	})
}

func TestValidateUnitPath(t *testing.T) {
	runValidateCases(t, func(s string) error { return validateUnitPath("path", s) }, []validateCase{
		{"/opt/gitops-agent/repo", false}, {"/srv/a.b/c_d+e@f", false},
		{"", true}, {"/", true}, {"rel/path", true}, {"/a b", true}, {"/a\nb", true},
		{"/a%h", true}, {"/a$x", true}, {`/a"b`, true}, {`/a\b`, true},
		{"/a/../b", true}, {"/a//b", true}, {"/a/", true}, {"/a:b", true},
		{"/" + strings.Repeat("a", 4096), true},
	})
}

func TestValidateSSHKeyPath(t *testing.T) {
	runValidateCases(t, validateSSHKeyPath, []validateCase{
		{"/home/pi/.ssh/id_ed25519", false}, {"/home/pi/my key", false},
		{"relative", true}, {"~/.ssh/id", true}, {"/a\nb", true}, {"/a\x00b", true},
		{"/a/../b", true}, {"/a\xffb", true}, {"", true},
	})
}

func TestValidateRepoURL(t *testing.T) {
	runValidateCases(t, validateRepoURL, []validateCase{
		{"git@github.com:o/r.git", false}, {"https://h/o/r.git", false}, {"ssh://git@h:22/o/r", false},
		{"", true}, {"-oProxyCommand=x", true}, {"a b", true}, {"a\nb", true},
		{"a\x00b", true}, {"a\tb", true}, {strings.Repeat("a", 2049), true},
	})
}

func TestValidateRoot(t *testing.T) {
	runValidateCases(t, validateRoot, []validateCase{
		{"", false}, {"/", false}, {"/tmp/x", false}, {"rel", true},
	})
}

func TestVersionRE(t *testing.T) {
	for _, v := range []string{"v1.2.3", "v10.0.1"} {
		if !versionRE.MatchString(v) {
			t.Errorf("%q should match", v)
		}
	}
	for _, v := range []string{"dev", "v1.2", "v1.2.3-4-gabc", "v1.2.3\n", "1.2.3", "v01.2.3"} {
		if versionRE.MatchString(v) {
			t.Errorf("%q should not match", v)
		}
	}
}
