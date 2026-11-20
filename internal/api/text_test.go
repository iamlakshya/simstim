package api

import (
	"math"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestCleanText(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
	}{
		{"plain", "auth-fix needs you", "auth-fix needs you"},
		{"empty", "", ""},
		{"unicode", "café · 日本語 → ✓", "café · 日本語 → ✓"},
		{"an OSC 52 write", "\x1b]52;c;aGVsbG8=\x07", "]52;c;aGVsbG8="},
		{"an OSC string ended by ST", "\x1b]0;title\x1b\\ok", "]0;title\\ok"},
		{"a CSI sequence", "\x1b[2J\x1b[31mred", "[2J[31mred"},
		{"C0 controls", "a\x00b\x01c\x07d\x08e\x1bf\x1fg", "abcdefg"},
		{"DEL", "a\x7fb", "ab"},
		{"C1 controls", "a\u0080b\u009bc\u009dd\u009ce\u009f", "abcde"},
		{"a raw C1 byte", "a\x9b31mb", "a\ufffd31mb"},
		{"bytes that are not UTF-8", "a\xff\xfeb", "a\ufffd\ufffdb"},
		{"lines", "one\ntwo\r\nthree\tfour\vfive\fsix", "one two  three four five six"},
		{"NEL and the separators", "a\u0085b\u2028c\u2029d", "a b c d"},
		{"bidirectional controls", "a\u202eb\u202dc\u202ad\u202ce\u2066f\u2069g\u200eh\u200fi\u061cj", "abcdefghij"},
		{"zero-width characters", "a\u200bb\u200cc\u200dd\u2060e\ufeff", "abcde"},
		{"invisible operators", "a\u2061b\u2064c", "abc"},
		{"soft hyphen", "pass\u00adword", "password"},
		{"variation selectors", "a\ufe0fb\ufe00c\U000e0100d", "abcd"},
		{"tag characters", "hi\U000e0001\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e007f", "hi"},
		{"unassigned tags", "a\U000e0000b\U000e0002c\U000e001fd", "abcd"},
		{"Hangul fillers", "a\u3164b\uffa0c", "abc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanText(c.in, 100); got != c.want {
				t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCleanTextCuts(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abc", 3, "abc"},
		{"日本語です", 2, "日本"},
		{"\x1b[31mabc", 3, "[31"},
		{"a\u200bb\u200bc\u200bd", 3, "abc"}, // the cut counts the runes it keeps
		{"abc", 0, ""},
		{"abc", -1, ""},
		// A large cap must not overflow the size of the result.
		{"a\x1bb", math.MaxInt, "ab"},
		{"a\x1bb", math.MaxInt / 3, "ab"},
		{"\u00e9\x07", math.MaxInt, "\u00e9"},
		{"plain", math.MaxInt, "plain"},
	} {
		if got := CleanText(c.in, c.max); got != c.want {
			t.Errorf("CleanText(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

// TestCleanTextKeepsCleanText checks that clean text comes back as it is,
// without a copy.
func TestCleanTextKeepsCleanText(t *testing.T) {
	s := strings.Repeat("auth-fix ~/src/app ", 10)
	if got := CleanText(s, 1000); got != s {
		t.Fatalf("CleanText changed clean text: %q", got)
	}
	if n := testing.AllocsPerRun(100, func() { CleanText(s, 1000) }); n != 0 {
		t.Errorf("CleanText of clean ASCII made %v allocations", n)
	}
}

// forbidden reports whether r is one of the characters that the spec says
// CleanText removes: C0 and C1 controls, DEL, bidirectional controls,
// zero-width characters and tag characters. It is written apart from
// CleanText, so the fuzz test checks one against the other.
func forbidden(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r <= 0x9f:
		return true
	case unicode.Is(unicode.Bidi_Control, r):
		return true
	case r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff:
		return true
	case r >= 0xe0000 && r <= 0xe007f:
		return true
	}
	return false
}

func FuzzCleanText(f *testing.F) {
	for _, s := range []string{
		"", "plain", "\x1b]52;c;aGk=\x07", "\x1b[31mred\x1b[0m", "a\u202eb", "a\u200db",
		"\U000e0068\U000e0069", "\xff\xfe", "line\nline", "日本語", "\u009b31m", "e\u0301",
	} {
		f.Add(s, 8)
	}
	f.Fuzz(func(t *testing.T, s string, maxRunes int) {
		maxRunes %= 64
		got := CleanText(s, maxRunes)
		if !utf8.ValidString(got) {
			t.Fatalf("CleanText(%q, %d) = %q, which is not UTF-8", s, maxRunes, got)
		}
		if n := utf8.RuneCountInString(got); n > max(maxRunes, 0) {
			t.Fatalf("CleanText(%q, %d) = %q, %d runes", s, maxRunes, got, n)
		}
		for _, r := range got {
			if forbidden(r) {
				t.Fatalf("CleanText(%q, %d) = %q, which keeps %U", s, maxRunes, got, r)
			}
		}
		if again := CleanText(got, maxRunes); again != got {
			t.Fatalf("CleanText is not stable: %q, then %q", got, again)
		}
		clean := utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxRunes
		for _, r := range s {
			clean = clean && !hidden(r) && !lineBreak(r)
		}
		if clean && got != s {
			t.Fatalf("CleanText(%q, %d) = %q, but the text was clean", s, maxRunes, got)
		}
	})
}

// Unclean is true for each rune that CleanText takes out or replaces.
func TestUnclean(t *testing.T) {
	for _, r := range []rune{0, '\t', '\n', 0x1b, 0x7f, 0x85, 0x9b, 0x200b, 0x202e, 0x2028, 0xfeff, 0xfe0f, 0xe0041} {
		if !Unclean(r) {
			t.Errorf("Unclean(%U) = false", r)
		}
		if got := CleanText("a"+string(r)+"b", 10); got == "a"+string(r)+"b" {
			t.Errorf("CleanText keeps %U, which Unclean names", r)
		}
	}
	for _, r := range []rune{' ', 'a', '~', 0xe9, 0x65e5, 0x2192, 0x1f600} {
		if Unclean(r) {
			t.Errorf("Unclean(%U) = true", r)
		}
	}
}

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"andvnnie": true, "a": true, "abcdefghijklmnopqrstuvwxyz234567": true,
		"": false, "s1": false, "n001": false, "ABC": false, "ab-cd": false, "ab\u200bcd": false,
		strings.Repeat("a", 33): false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}
