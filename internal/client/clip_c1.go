package client

import (
	"encoding/binary"
	"unicode/utf8"
)

// xterm's reading (spec 7.6) has C1 controls on: a C1 control acts as its
// 7-bit ESC form in every state, ground too. That is its UTF-8 form, C2 80
// to C2 9F, and a raw byte from 0x80 to 0x9F that continues no UTF-8
// character. A byte inside a valid character, as in "❯" or "Ü", is text.
// The xterm reader turns every C1 control into its ESC form
// (c1Translator), then parses 7-bit. Ghostty's reader reads the bytes as
// they are. Both read every byte from the start of the attach.

// c1At reports whether b starts with a C1 control, and how many bytes it
// takes. ok is false when b may start a character that the next bytes end.
func c1At(b []byte) (c1 bool, n int, ok bool) {
	c := b[0]
	switch {
	case c < 0x80:
		return false, 1, true
	case c == 0xC2 && len(b) == 1:
		return false, 0, false
	case c == 0xC2 && b[1] >= 0x80 && b[1] <= 0x9F:
		return true, 2, true
	case !utf8.FullRune(b):
		return false, 0, false
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size == 1 {
		return c <= 0x9F, 1, true // a byte that continues no character
	}
	return false, size, true
}

// c1Index is the index of the first C1 control in b, or of a character
// that the end of b cuts, or -1. It passes ASCII 8 bytes at a time, and
// whole characters one at a time, so output with no C1 control costs one
// pass.
func c1Index(b []byte) int {
	for i := 0; i < len(b); {
		for i+8 <= len(b) && binary.LittleEndian.Uint64(b[i:])&0x8080808080808080 == 0 {
			i += 8
		}
		for i < len(b) && b[i] < 0x80 {
			i++
		}
		if i >= len(b) {
			break
		}
		c1, n, ok := c1At(b[i:])
		if c1 || !ok {
			return i
		}
		i += n
	}
	return -1
}

// c1Translator turns each C1 control of xterm's reading into its 7-bit ESC
// form, in a stream that arrives in parts.
type c1Translator struct {
	pend []byte // the start of a character that the next part may end
	in   []byte
	out  []byte
}

// translate returns b as xterm reads it: b itself when it holds no C1
// control, which is the common case. The slice is valid until the next
// call. The start of a character that the next part may end waits.
func (t *c1Translator) translate(b []byte) []byte {
	in := b
	if len(t.pend) > 0 {
		t.in = append(append(t.in[:0], t.pend...), b...)
		in, t.pend = t.in, t.pend[:0]
	}
	i := c1Index(in)
	if i < 0 {
		return in
	}
	t.out = append(t.out[:0], in[:i]...)
	for i < len(in) {
		c1, n, ok := c1At(in[i:])
		switch {
		case !ok:
			t.pend = append(t.pend[:0], in[i:]...)
			return t.out
		case c1:
			t.out = append(t.out, 0x1b, in[i+n-1]-0x40)
		default:
			t.out = append(t.out, in[i:i+n]...)
		}
		i += n
		j := c1Index(in[i:])
		if j < 0 {
			j = len(in) - i
		}
		t.out = append(t.out, in[i:i+j]...)
		i += j
	}
	return t.out
}
