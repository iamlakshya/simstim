package host

import (
	"strconv"
	"strings"
)

// CompareDebianVersion compares two Debian package version strings according to
// Debian Policy section 5.6.12 ([epoch:]upstream_version[-debian_revision]).
// It returns -1 if a < b, 0 if a == b, and +1 if a > b.
func CompareDebianVersion(a, b string) int {
	ea, ua, ra := splitDebianVersion(a)
	eb, ub, rb := splitDebianVersion(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := compareDebianPart(ua, ub); c != 0 {
		return c
	}
	return compareDebianPart(ra, rb)
}

func splitDebianVersion(v string) (epoch int, upstream, revision string) {
	v = strings.TrimSpace(v)
	if idx := strings.IndexByte(v, ':'); idx >= 0 {
		if e, err := strconv.Atoi(v[:idx]); err == nil && e >= 0 {
			epoch = e
			v = v[idx+1:]
		}
	}
	if idx := strings.LastIndexByte(v, '-'); idx >= 0 {
		upstream = v[:idx]
		revision = v[idx+1:]
	} else {
		upstream = v
	}
	return epoch, upstream, revision
}

// compareDebianPart implements dpkg's verrevcmp algorithm: alternating non-digit
// runs (where '~' sorts before everything including end-of-string, letters sort
// before non-letters) and digit runs compared numerically.
func compareDebianPart(a, b string) int {
	ia, ib := 0, 0
	for ia < len(a) || ib < len(b) {
		var cmp int
		ia, ib, cmp = compareDebianNonDigits(a, b, ia, ib)
		if cmp != 0 {
			return cmp
		}
		ia, ib, cmp = compareDebianDigits(a, b, ia, ib)
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

func compareDebianNonDigits(a, b string, ia, ib int) (nextA, nextB, cmp int) {
	for (ia < len(a) && !isASCIIDigit(a[ia])) || (ib < len(b) && !isASCIIDigit(b[ib])) {
		ca, cb := 0, 0
		if ia < len(a) && !isASCIIDigit(a[ia]) {
			ca = debianCharOrder(a[ia])
			ia++
		}
		if ib < len(b) && !isASCIIDigit(b[ib]) {
			cb = debianCharOrder(b[ib])
			ib++
		}
		if ca != cb {
			return ia, ib, cmpInt(ca, cb)
		}
	}
	return ia, ib, 0
}

func compareDebianDigits(a, b string, ia, ib int) (nextA, nextB, cmp int) {
	da, nextA := scanDebianDigits(a, ia)
	db, nextB := scanDebianDigits(b, ib)
	if len(da) != len(db) {
		return nextA, nextB, cmpInt(len(da), len(db))
	}
	if da != db {
		if da < db {
			return nextA, nextB, -1
		}
		return nextA, nextB, 1
	}
	return nextA, nextB, 0
}

func scanDebianDigits(s string, i int) (digits string, next int) {
	for i < len(s) && s[i] == '0' {
		i++
	}
	start := i
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
	}
	return s[start:i], i
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// debianCharOrder assigns dpkg's sort weight to a non-digit character:
// '~' (-1) < end-of-part (0) < ASCII letters (byte value) < other bytes (+256).
func debianCharOrder(c byte) int {
	if c == '~' {
		return -1
	}
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return int(c)
	}
	return int(c) + 256
}
