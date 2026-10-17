package host

import (
	"bytes"
	"encoding/binary"
)

// text reads text and controls up to the next ESC, as a byte reader of the
// router (screen_route.go). Most of the output is text between sequences, so
// it finds the next ESC first, and skips the plain text before it. That text
// goes on as it is.
func (m *screenModel) text(i int) int {
	rt := &m.rt
	p := rt.p
	if rt.runeLeft > 0 {
		n := min(rt.runeLeft, len(p)-i)
		rt.runeLeft -= n
		return i + n - 1
	}
	end := len(p)
	if j := bytes.IndexByte(p[i:], 0x1b); j >= 0 {
		end = i + j
	}
	for i < end {
		i += plainText(p[i:end])
		if i < end {
			i = m.textByte(i) + 1
		}
	}
	if i > end || end == len(p) {
		// A character took the ESC, or the read ended.
		return i - 1
	}
	rt.state, rt.s = routeEsc, end
	return end
}

// plainText returns how many bytes at the start of p are plain text: ASCII,
// and whole UTF-8 characters as vt reads them. It reads eight ASCII bytes at
// a time.
func plainText(p []byte) int {
	i := 0
	for i < len(p) {
		if i+8 <= len(p) && binary.LittleEndian.Uint64(p[i:])&0x8080808080808080 == 0 {
			i += 8
			continue
		}
		switch b := p[i]; {
		case b < 0x80:
			i++
		case b >= 0xc2 && b < 0xf5 && i+runeLen(b) <= len(p):
			i += runeLen(b)
		default:
			return i
		}
	}
	return i
}

// textByte reads one byte that is not plain text as vt does, and returns the
// index of the last byte that it read. vt reads the bytes after a lead byte
// as part of the character, whatever they are, even ESC. It acts on a C1
// control, which the router drops.
func (m *screenModel) textByte(i int) int {
	rt := &m.rt
	p := rt.p
	switch b := p[i]; {
	case b >= 0xc2 && b < 0xf5:
		n := runeLen(b) - 1
		if i+n >= len(p) {
			rt.runeLeft = i + n - (len(p) - 1)
			return len(p) - 1
		}
		return i + n
	case b >= 0x80 && b < 0xa0:
		m.flushTo(i)
		rt.r = i + 1
	}
	return i
}

// runeLen returns how many bytes vt reads for a UTF-8 character that starts
// with the lead byte b.
func runeLen(b byte) int {
	switch {
	case b >= 0xf0:
		return 4
	case b >= 0xe0:
		return 3
	default:
		return 2
	}
}
