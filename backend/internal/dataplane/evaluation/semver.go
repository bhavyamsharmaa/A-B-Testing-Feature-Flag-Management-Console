package evaluation

import (
	"strconv"
	"strings"
)

// semver is a parsed Semantic Versioning 2.0.0 version. Build metadata is
// dropped: it takes no part in precedence.
type semver struct {
	major, minor, patch uint64
	pre                 []string // dot-separated pre-release identifiers
}

// maxSemverLen bounds what is parsed; no real version is this long.
const maxSemverLen = 128

// parseSemver accepts exactly the grammar of semver.org: MAJOR.MINOR.PATCH,
// optional -prerelease, optional +build. No "v" prefix, no leading zeros in
// numeric parts, no missing parts. Anything else is not a version (ok=false).
func parseSemver(s string) (v semver, ok bool) {
	if s == "" || len(s) > maxSemverLen {
		return semver{}, false
	}
	core := s
	var pre, build string
	var hasPre, hasBuild bool
	if i := strings.IndexByte(core, '+'); i >= 0 {
		core, build, hasBuild = core[:i], core[i+1:], true
	}
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core, pre, hasPre = core[:i], core[i+1:], true
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]uint64
	for i, p := range parts {
		n, ok := parseNumericIdentifier(p)
		if !ok {
			return semver{}, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	if hasPre {
		ids := strings.Split(pre, ".")
		for _, id := range ids {
			if !validIdentifier(id, true) {
				return semver{}, false
			}
		}
		v.pre = ids
	}
	if hasBuild {
		for _, id := range strings.Split(build, ".") {
			if !validIdentifier(id, false) {
				return semver{}, false
			}
		}
	}
	return v, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseNumericIdentifier: digits only, no leading zero (except "0" itself),
// and small enough for uint64.
func parseNumericIdentifier(s string) (uint64, bool) {
	if !isDigits(s) || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// validIdentifier checks one dot-separated identifier: [0-9A-Za-z-]+, and for
// pre-release identifiers a purely numeric one may not have a leading zero.
func validIdentifier(id string, pre bool) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	if pre && isDigits(id) && len(id) > 1 && id[0] == '0' {
		return false
	}
	return true
}

// compare returns -1, 0 or 1 by semver precedence.
func (a semver) compare(b semver) int {
	for _, p := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	// A version without a pre-release is greater than the same one with it.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePre(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	aNum, bNum := isDigits(a), isDigits(b)
	switch {
	case aNum && bNum:
		// Numeric identifiers have no leading zeros, so a longer one is larger;
		// this also orders identifiers too long for any integer type.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case aNum:
		return -1 // numeric identifiers sort before alphanumeric ones
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}
