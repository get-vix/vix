// Package update implements GitHub-release update detection and the in-app
// upgrade flow: a once-per-day check against the latest published release, a
// version comparator, install-method detection, and the command used to
// perform the upgrade.
package update

import (
	"strconv"
	"strings"
)

// IsDev reports whether v is an unstamped local build ("dev" or empty). Such
// builds never trigger an update prompt — there is no meaningful release to
// compare against.
func IsDev(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == "dev"
}

// NewerThan reports whether release tag a is strictly newer than tag b using a
// minimal semantic-version comparison. Both may carry a leading "v". A pre-build
// suffix ("-rc1") is ignored beyond the numeric fields. A "dev"/empty b is
// treated as older than any real tag; a "dev"/empty a is never newer.
func NewerThan(a, b string) bool {
	if IsDev(a) {
		return false
	}
	if IsDev(b) {
		return true
	}
	return compare(a, b) > 0
}

// compare returns -1, 0, or 1 comparing the numeric dotted fields of two
// version strings. Missing fields count as 0, so "v1.4" == "v1.4.0".
func compare(a, b string) int {
	fa := fields(a)
	fb := fields(b)
	n := len(fa)
	if len(fb) > n {
		n = len(fb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(fa) {
			x = fa[i]
		}
		if i < len(fb) {
			y = fb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// fields parses the leading numeric dotted components of a version tag,
// stopping at the first non-numeric component (e.g. a "-rc1" pre-release tail).
func fields(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
