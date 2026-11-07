package selfupdate

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              int
		wantErr           bool
	}{
		{"v1.2.3", "v1.2.4", -1, false},
		{"v1.2.3", "v1.2.3", 0, false},
		{"v1.2.4", "v1.2.3", 1, false},
		{"v1.9.0", "v1.10.0", -1, false},
		{"v2.0.0", "v1.99.99", 1, false},
		{"v0.0.1", "v0.0.1", 0, false},
		{"", "v1.2.3", 0, true},
		{"dev", "v1.2.3", 0, true},
		{"v1.2", "v1.2.3", 0, true},
		{"1.2.3", "v1.2.3", 0, true},
		{"v1.2.3-rc1", "v1.2.3", 0, true},
		{"v1.2.3+b", "v1.2.3", 0, true},
		{"v01.2.3", "v1.2.3", 0, true},
		{"v1.2.3\n", "v1.2.3", 0, true},
		{"v1.2.99999999999999999999999999999999999999", "v1.2.3", 0, true},
		{"v1.2.3", "dev", 0, true},
		{"v1.2.3", "", 0, true},
	}
	for _, tc := range cases {
		got, err := compareVersions(tc.installed, tc.latest)
		if (err != nil) != tc.wantErr {
			t.Errorf("compareVersions(%q, %q) error = %v, wantErr %v", tc.installed, tc.latest, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.installed, tc.latest, got, tc.want)
		}
	}
}
