package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.2", "1.2.0", 0},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-rc.2", "1.2.3-rc.1", 1},
		// Numeric prerelease identifiers compare numerically (semver §11.4.1),
		// which a byte-wise comparison gets backwards.
		{"1.2.3-rc.10", "1.2.3-rc.9", 1},
		{"1.2.3-rc.9", "1.2.3-rc.10", -1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3-rc.1", "1.2.3-rc", 1},
		{"1.2.3-1", "1.2.3-alpha", -1},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	for _, v := range []string{"1.2.3", "0.1.0", "10.0.1", "1.2.3-rc.1"} {
		if !IsRelease(v) {
			t.Errorf("IsRelease(%q) = false, want true", v)
		}
	}
	notReleases := []string{
		"",                        // ldflags forgotten
		"dev",                     // go run / bare go build
		"master-3577643",          // version.Public() leaking in
		"v0.6.3",                  // server tag shape
		"v0.6.3-2-g3577643",       // make build via git describe
		"v0.6.3-2-g3577643-dirty", //
		"0.1.0-3-gabcdef",         // describe without the v prefix
		"1.2.3-dirty",             // dirty worktree at a tag
		"placard-cli",             // anything that is not a version at all
	}
	for _, v := range notReleases {
		if IsRelease(v) {
			t.Errorf("IsRelease(%q) = true, want false", v)
		}
	}
}
