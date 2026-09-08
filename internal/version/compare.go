package version

import (
	"strconv"
	"strings"
)

// Compare does a numeric dotted comparison of two release versions, treating
// any prerelease suffix (1.2.0-rc.1) as lower than the same release version.
// Missing components read as 0, so "1.2" and "1.2.0" compare equal.
//
// Both the CLI's self-update policy and the server's min_cli_version gate run
// through this: two implementations would eventually disagree about which
// build is "older", and the server would then reject a CLI that considers
// itself current.
func Compare(a, b string) int {
	aCore, aPre := splitPrerelease(a)
	bCore, bPre := splitPrerelease(b)
	aParts, bParts := strings.Split(aCore, "."), strings.Split(bCore, ".")
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		an, bn := 0, 0
		if i < len(aParts) {
			an, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bn, _ = strconv.Atoi(bParts[i])
		}
		if an != bn {
			if an > bn {
				return 1
			}
			return -1
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "":
		return 1 // a release beats its own prerelease
	case bPre == "":
		return -1
	}
	return comparePrerelease(aPre, bPre)
}

// comparePrerelease orders two prerelease strings the way semver does:
// dot-separated identifiers, compared one by one, numeric ones numerically so
// rc.10 outranks rc.9, and a longer run of identifiers outranking its own
// prefix.
func comparePrerelease(a, b string) int {
	aIDs, bIDs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aIDs) && i < len(bIDs); i++ {
		an, aNum := strconv.Atoi(aIDs[i])
		bn, bNum := strconv.Atoi(bIDs[i])
		switch {
		case aNum == nil && bNum == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aNum == nil:
			return -1 // numeric identifiers rank below alphanumeric ones
		case bNum == nil:
			return 1
		case aIDs[i] != bIDs[i]:
			if aIDs[i] > bIDs[i] {
				return 1
			}
			return -1
		}
	}
	return sign(len(aIDs) - len(bIDs))
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

func splitPrerelease(v string) (string, string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// IsRelease reports whether v is a published release version (1.2.3,
// 1.2.3-rc.1) rather than a local or development build. Everything a
// non-release build can carry is rejected: the empty string and "dev" from a
// bare `go build`, the "master-<commit>" ops shape, git describe's
// "-<n>-g<sha>" tail, a "-dirty" worktree, and the leading "v" that marks a
// SERVER tag (CLI releases are tagged cli/vX.Y.Z and carry a bare semver).
//
// It is what keeps the min_cli_version gate off a developer running an
// unreleased build, and what config validation accepts as a minimum.
func IsRelease(v string) bool {
	core, pre := splitPrerelease(strings.TrimSpace(v))
	if core == "" {
		return false
	}
	for _, part := range strings.Split(core, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	// A release prerelease is dot-separated alphanumerics. A hyphen inside it
	// is git describe's commit tail, never a published version.
	if pre == "" {
		return true
	}
	if pre == "dirty" || strings.ContainsRune(pre, '-') {
		return false
	}
	for _, r := range pre {
		if r != '.' && !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
