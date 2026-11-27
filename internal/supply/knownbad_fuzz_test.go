package supply

import (
	"math/big"
	"strings"
	"testing"
)

// FuzzBelow checks Below against refBelow, a second reading of the grammar
// written by hand, with no regular expression, and with big numbers.
func FuzzBelow(f *testing.F) {
	for _, s := range [][2]string{{"51.1", "51.2"}, {"6.18.9", "6.18.10"}, {"release-20261102.0", "20261103.0"},
		{"0.6.0-1a2b3c4d", "0.6.0"}, {"051.02", "51.2"}, {"3c83c21", "0.2.0"}, {"0.2.0-rc.1", "0.2.0"},
		{"99999999999999999999.1", "1.0"}, {"1234567", "0.2.0"}, {"0.1.2-45-gabc1234-dirty", "0.2.0"},
		{"6.18.9-arch1-1", "6.18.9"}, {"v1.2.3.4", "v1.2.3.4"}, {"0.2.0-SNAPSHOT-x", "0.2.0"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, v, before string) {
		if got, want := Below(v, before), refBelow(v, before); got != want {
			t.Errorf("Below(%q, %q) = %v, want %v", v, before, got, want)
		}
		if _, kind, ok := refRead(v); ok && kind == "" && Below(v, v) {
			t.Errorf("Below(%q, %q) holds", v, v)
		}
	})
}

// refBelow is the rule of spec 20.9 on refRead's reading.
func refBelow(v, before string) bool {
	x, kind, ok := refRead(v)
	b, bkind, bok := refRead(before)
	if !ok || !bok || bkind != "" {
		return true
	}
	for i := range max(len(x), len(b)) {
		p, q := big.NewInt(0), big.NewInt(0)
		if i < len(x) {
			p = x[i]
		}
		if i < len(b) {
			q = b[i]
		}
		if c := p.Cmp(q); c != 0 {
			return c < 0
		}
	}
	return kind == "pre"
}

// refRead reads a version by the grammar of spec 20.9: an optional
// "release-" or "v", two to four dotted numbers, and at most one suffix. It
// gives the numbers and the kind of suffix: "", "pre", "describe" or
// "build".
func refRead(s string) ([]*big.Int, string, bool) {
	if rest, ok := strings.CutPrefix(s, "release-"); ok {
		s = rest
	} else {
		s = strings.TrimPrefix(s, "v")
	}
	core, suffix, dash := strings.Cut(s, "-")
	fields := strings.Split(core, ".")
	if len(fields) < 2 || len(fields) > 4 {
		return nil, "", false
	}
	nums := make([]*big.Int, len(fields))
	for i, f := range fields {
		if !allIn(f, "0123456789") || f == "" {
			return nil, "", false
		}
		nums[i], _ = new(big.Int).SetString(f, 10)
	}
	switch {
	case !dash:
		return nums, "", true
	case refPre(strings.ToLower(suffix)):
		return nums, "pre", true
	case refDescribe(suffix), refBuild(suffix):
		return nums, "build", true
	}
	return nil, "", false
}

func refPre(s string) bool {
	for _, w := range []string{"rc", "alpha", "beta", "pre", "dev", "snapshot"} {
		if rest, ok := strings.CutPrefix(s, w); ok {
			return allIn(rest, "0123456789abcdefghijklmnopqrstuvwxyz.-")
		}
	}
	return false
}

// refDescribe reads "<n>-g<hex>" and "<n>-g<hex>-dirty", where hex has 4 to 40 digits.
func refDescribe(s string) bool {
	f := strings.Split(s, "-")
	if len(f) == 3 && f[2] == "dirty" {
		f = f[:2]
	}
	return len(f) == 2 && f[0] != "" && allIn(f[0], "0123456789") && strings.HasPrefix(f[1], "g") &&
		len(f[1]) >= 5 && len(f[1]) <= 41 && allIn(f[1][1:], "0123456789abcdef")
}

// refBuild reads a distribution's build, "<n>", "arch<n>" or
// "arch<n>-<n>", and the helper's build, 8 hex digits.
func refBuild(s string) bool {
	if len(s) == 8 && allIn(s, "0123456789abcdef") {
		return true
	}
	if rest, ok := strings.CutPrefix(s, "arch"); ok {
		n, m, two := strings.Cut(rest, "-")
		return n != "" && allIn(n, "0123456789") && (!two || m != "" && allIn(m, "0123456789"))
	}
	return s != "" && allIn(s, "0123456789")
}

func allIn(s, set string) bool {
	for _, r := range s {
		if !strings.ContainsRune(set, r) {
			return false
		}
	}
	return true
}
