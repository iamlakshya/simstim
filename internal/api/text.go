package api

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanText removes C0 and C1 controls, DEL, bidirectional controls,
// zero-width characters and Unicode tag characters from s, and cuts it
// to maxRunes runes. The client draws the text that a host sends through
// it. It is for display only: it changes some text that has a meaning, so
// a value that goes back to a host, or that names a file or a session,
// stays as the host sent it.
//
// The characters it removes are Unicode's format characters, its variation
// selectors and its other default ignorable code points. Most of them draw
// as nothing, such as U+200B and U+FEFF. Some are visible or carry meaning:
// U+200C and U+200D join letters and emoji, U+FE0F picks an emoji's look,
// and U+0600 to U+0605 draw. A tab, a line feed, a vertical tab, a form
// feed, a carriage return, U+0085 and the line and paragraph separators
// become one space each, so that two lines do not run into one word. A
// byte that is not UTF-8 becomes U+FFFD. With maxRunes of zero or less,
// the result is empty.
func CleanText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if len(s) <= maxRunes && plainASCII(s) {
		return s
	}
	// A string has no more runes than bytes. The cap keeps the size below
	// from overflowing.
	maxRunes = min(maxRunes, len(s))
	var b strings.Builder
	b.Grow(min(len(s), maxRunes*utf8.UTFMax))
	n := 0
	// A range over a string gives U+FFFD for each byte that is not UTF-8.
	for _, r := range s {
		if n == maxRunes {
			break
		}
		switch {
		case lineBreak(r):
			r = ' '
		case hidden(r):
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Unclean reports whether CleanText takes r out of a text, or puts a space
// in its place.
func Unclean(r rune) bool { return lineBreak(r) || hidden(r) }

// plainASCII reports whether s holds only printable ASCII, which CleanText
// keeps as it is.
func plainASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// lineBreak reports whether r ends a line or moves to a tab stop. CleanText
// puts a space in its place.
func lineBreak(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// hidden reports whether r is one that CleanText removes: C0 and C1
// controls and DEL, format characters (bidirectional controls, zero-width
// characters and tags among them), variation selectors, and the other
// default ignorable code points, which hold the rest of the tag block.
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Bidi_Control,
		unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point)
}

// SessionIDAlphabet holds the characters of a session ID: lower-case
// base32, as every host helper makes them.
const SessionIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// ValidSessionID reports whether id has the form of a session ID: 1 to 32
// characters of SessionIDAlphabet.
func ValidSessionID(id string) bool {
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune(SessionIDAlphabet, c) {
			return false
		}
	}
	return true
}
