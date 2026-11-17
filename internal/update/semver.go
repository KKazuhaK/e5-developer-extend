// Package update finds out whether a newer release exists and installs it.
// Nothing here runs a downloaded program before its signature and checksum
// have been verified (docs/design/update.md).
package update

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is a semantic version (https://semver.org), without build metadata.
type Version struct {
	Major, Minor, Patch uint64
	// Pre holds the dot-separated pre-release identifiers, empty for a release.
	Pre []string
}

// ParseVersion reads "1.2.3", "v1.2.3" or "1.2.3-rc.1"; build metadata ("+...") is ignored.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	nums := strings.Split(core, ".")
	if len(nums) != 3 {
		return Version{}, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", s)
	}
	var n [3]uint64
	for i, p := range nums {
		v, err := parseNumeric(p)
		if err != nil {
			return Version{}, fmt.Errorf("%q: %w", s, err)
		}
		n[i] = v
	}
	v := Version{Major: n[0], Minor: n[1], Patch: n[2]}
	if hasPre {
		if pre == "" {
			return Version{}, fmt.Errorf("%q: empty pre-release", s)
		}
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return Version{}, fmt.Errorf("%q: empty pre-release identifier", s)
			}
			for _, c := range id {
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
					return Version{}, fmt.Errorf("%q: bad character %q in the pre-release", s, c)
				}
			}
			if isNumeric(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, fmt.Errorf("%q: numeric pre-release identifier %q has a leading zero", s, id)
			}
			v.Pre = append(v.Pre, id)
		}
	}
	return v, nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseNumeric(s string) (uint64, error) {
	if !isNumeric(s) {
		return 0, errors.New("not a number: " + strconv.Quote(s))
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, errors.New("leading zero in " + strconv.Quote(s))
	}
	return strconv.ParseUint(s, 10, 64)
}

// String formats v as "1.2.3" or "1.2.3-rc.1".
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// IsPrerelease reports whether v has a pre-release part.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or 1 as v is older than, equal to or newer than o.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if c := cmpUint(p[0], p[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1 // a release is newer than its pre-releases
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := cmpIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// cmpIdent orders pre-release identifiers: numbers numerically and below
// words, words in ASCII order.
func cmpIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		return cmpUint(x, y)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// IsDevBuild reports whether s names a development build: "dev", something
// that is not a version, or a "dev" pre-release such as 0.0.0-dev.g1234567
// (what the packaging scripts give a build that is not a tagged release).
// Development builds never check for updates on their own.
func IsDevBuild(s string) bool {
	v, err := ParseVersion(s)
	if err != nil {
		return true
	}
	return len(v.Pre) > 0 && v.Pre[0] == "dev"
}

// Newer reports whether latest is a newer version than current. It is false
// when either does not parse or current is a development build.
func Newer(current, latest string) bool {
	if IsDevBuild(current) {
		return false
	}
	c, err := ParseVersion(current)
	if err != nil {
		return false
	}
	l, err := ParseVersion(latest)
	if err != nil {
		return false
	}
	return l.Compare(c) > 0
}
