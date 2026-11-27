package supply

import (
	"cmp"
	"regexp"
	"strings"
)

// The grammar of a version that the known-bad list compares (isolation spec
// 20.9). It is an allow-list: an optional "release-" or "v", two to four
// dotted numbers, and at most one suffix of the forms below. Anything else
// cannot be read, and counts as below every before, so a check fails
// closed. That covers a bare commit of any shape, such as 3c83c21, abc1234
// or the all-digit 1234567, with or without -dirty.
var (
	versionCore = regexp.MustCompile(`^(?:release-|v)?([0-9]+(?:\.[0-9]+){1,3})(-.*)?$`)
	// A pre-release, as in 51.2-rc1 or 0.2.0-SNAPSHOT-1a2b3c4d, of any case.
	// It comes before the release of its number.
	suffixPre = regexp.MustCompile(`^-(?:rc|alpha|beta|pre|dev|snapshot)[0-9a-z.-]*$`)
	// git describe after a tag: 0.1.2-45-g3f90e48, and -dirty. It comes
	// after the tag.
	suffixDescribe = regexp.MustCompile(`^-[0-9]+-g[0-9a-f]{4,40}(?:-dirty)?$`)
	// A distribution's build of a release, such as -1 or -arch1, or the
	// helper's own build, 0.6.0-1a2b3c4d: the release, a dash and the first 8
	// hex digits of the helpers' digest (client.HelperVersion). Each is the
	// release of its number.
	suffixBuild = regexp.MustCompile(`^-(?:[0-9]+|arch[0-9]+(?:-[0-9]+)?|[0-9a-f]{8})$`)
)

// The kinds of suffix.
const (
	suffixNone = iota
	suffixPreRelease
	suffixAfter
	suffixSame
)

// version is a version read by the grammar.
type version struct {
	parts  []string // the dotted numbers, without leading zeros: "" stands for 0
	suffix int
}

// readVersion reads s by the grammar, or reports false.
func readVersion(s string) (version, bool) {
	m := versionCore.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	v := version{parts: strings.Split(m[1], ".")}
	for i, p := range v.parts {
		v.parts[i] = strings.TrimLeft(p, "0")
	}
	switch rest := m[2]; {
	case rest == "":
		v.suffix = suffixNone
	case suffixPre.MatchString(strings.ToLower(rest)):
		v.suffix = suffixPreRelease
	case suffixDescribe.MatchString(rest):
		v.suffix = suffixAfter
	case suffixBuild.MatchString(rest):
		v.suffix = suffixSame
	default:
		return version{}, false
	}
	return v, true
}

// cleanVersion reports whether s is the version of a release: it reads,
// and has no suffix. A before must be one.
func cleanVersion(s string) bool {
	v, ok := readVersion(s)
	return ok && v.suffix == suffixNone
}

// compareParts compares two versions part by part, as numbers of any
// length. A missing part counts as 0.
func compareParts(a, b []string) int {
	for i := range max(len(a), len(b)) {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if c := cmp.Compare(len(x), len(y)); c != 0 {
			return c
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// Below reports whether v is below before, as dotted numbers: 51.1 is below
// 51.2, and 6.18.9 below 6.18.10. A pre-release of before, such as
// 51.2-rc1, is below it. A git describe version and a build of before are
// not. Every form outside the grammar counts as below, so a check that reads
// it fails closed. So does a before that is not the version of a release.
func Below(v, before string) bool {
	x, ok := readVersion(v)
	b, bok := readVersion(before)
	if !ok || !bok || b.suffix != suffixNone {
		return true
	}
	c := compareParts(x.parts, b.parts)
	return c < 0 || c == 0 && x.suffix == suffixPreRelease
}

// stronger reports whether a covers more than b, two entries for one
// advisory, kind and name: an unresolved entry beats any before, and a
// higher before beats a lower one. A before that cannot be read covers
// every version, as Below reads it.
func stronger(a, b Advisory) bool {
	ra, rb := rank(a), rank(b)
	if ra == nil || rb == nil {
		return ra == nil && rb != nil
	}
	return compareParts(ra, rb) > 0
}

// rank is the before of an entry as dotted numbers, or nil for an entry
// that covers every version.
func rank(a Advisory) []string {
	v, ok := readVersion(a.Before)
	if a.Unresolved || !ok || v.suffix != suffixNone {
		return nil
	}
	return v.parts
}
