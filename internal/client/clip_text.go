package client

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// The paste guard compares texts by the SHA-256 of their key: the text
// without spaces, controls, invisible characters, quotes and backslashes,
// and in lower case. A path that a session wrote then matches the same
// path in a paste, however the session quoted, padded or split it, and
// whatever the terminal took out of it. The guard keeps only these hashes,
// never the text.

// keyRune maps one character of a text to its key, or drops it (-1).
func keyRune(r rune) rune {
	switch {
	case unicode.IsSpace(r), unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\'', r == '"', r == '\\':
		return -1
	default:
		return unicode.ToLower(r)
	}
}

// textHash hashes the key of a text that arrives in parts. It decodes UTF-8
// across the parts exactly as ranging over the whole text would: a byte
// that starts no character counts as U+FFFD.
type textHash struct {
	h    hash.Hash
	pend []byte // the start of a character that the next part ends
	keys int    // characters that the key kept
	enc  [utf8.UTFMax]byte
}

func newTextHash() *textHash { return &textHash{h: sha256.New()} }

// write adds a part of the text.
func (t *textHash) write(b []byte) {
	for len(t.pend) > 0 && len(b) > 0 {
		for len(b) > 0 && !utf8.FullRune(t.pend) {
			t.pend, b = append(t.pend, b[0]), b[1:]
		}
		if !utf8.FullRune(t.pend) {
			return
		}
		r, n := utf8.DecodeRune(t.pend)
		t.add(r)
		t.pend = append(t.pend[:0], t.pend[n:]...)
	}
	for len(b) > 0 {
		if !utf8.FullRune(b) {
			t.pend = append(t.pend[:0], b...)
			return
		}
		r, n := utf8.DecodeRune(b)
		t.add(r)
		b = b[n:]
	}
}

func (t *textHash) add(r rune) {
	if k := keyRune(r); k >= 0 {
		n := utf8.EncodeRune(t.enc[:], k)
		t.h.Write(t.enc[:n])
		t.keys++
	}
}

// sum is the hash of the key so far, with an unfinished character counted
// as the text would end here. It does not change the hash, so more parts
// can follow. ok is false when the key is empty.
func (t *textHash) sum() (s [32]byte, ok bool) {
	if len(t.pend) == 0 {
		if t.keys == 0 {
			return s, false
		}
		copy(s[:], t.h.Sum(nil))
		return s, true
	}
	c := t.clone()
	for len(c.pend) > 0 {
		r, n := utf8.DecodeRune(c.pend)
		c.add(r)
		c.pend = c.pend[n:]
	}
	return c.sum()
}

func (t *textHash) clone() *textHash {
	c := &textHash{h: sha256.New(), pend: append([]byte(nil), t.pend...), keys: t.keys}
	st, err := t.h.(interface{ MarshalBinary() ([]byte, error) }).MarshalBinary()
	if err == nil {
		err = c.h.(interface{ UnmarshalBinary([]byte) error }).UnmarshalBinary(st)
	}
	if err != nil {
		panic("sha256 state: " + err.Error()) // the standard library's digest always marshals
	}
	return c
}

// keySums counts the calls of keySum, for the tests.
var keySums atomic.Int64

// keySum is the hash of the key of a whole text, or false when the key is empty.
func keySum(s string) ([32]byte, bool) {
	keySums.Add(1)
	t := newTextHash()
	t.write([]byte(s))
	return t.sum()
}

// b64Stream decodes base64 that arrives in parts, as leniently as any
// terminal might: it skips bytes outside the alphabet, and padding ends a
// run that it decodes on its own. With url set, "-" and "_" count as "+"
// and "/".
type b64Stream struct {
	url  bool
	acc  uint32
	n    int
	sawU bool // a "-" or "_" came
	out  []byte
}

var b64Val = func() (v [256]int8) {
	for i := range v {
		v[i] = -1
	}
	const std = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := range len(std) {
		v[std[i]] = int8(i)
	}
	return v
}()

// write decodes a part, and returns the bytes it gives. The slice is valid
// until the next call.
func (d *b64Stream) write(b []byte) []byte {
	d.out = d.out[:0]
	for _, c := range b {
		v := b64Val[c]
		switch {
		case v >= 0:
		case c == '-' || c == '_':
			d.sawU = true
			if !d.url {
				continue
			}
			v = 62
			if c == '_' {
				v = 63
			}
		case c == '=':
			d.flush()
			continue
		default:
			continue
		}
		d.acc = d.acc<<6 | uint32(v)
		d.n++
		if d.n == 4 {
			var q [4]byte
			binary.BigEndian.PutUint32(q[:], d.acc<<8)
			d.out = append(d.out, q[:3]...)
			d.acc, d.n = 0, 0
		}
	}
	return d.out
}

// end decodes what is left, and returns the bytes it gives.
func (d *b64Stream) end() []byte {
	d.out = d.out[:0]
	d.flush()
	return d.out
}

// flush decodes a run that padding or the end cut short: two characters
// give one byte, three give two, and one gives none.
func (d *b64Stream) flush() {
	if d.n >= 2 {
		// n characters hold 6n bits: shift them to the top of 32.
		var q [4]byte
		binary.BigEndian.PutUint32(q[:], d.acc<<(32-6*d.n))
		d.out = append(d.out, q[:d.n-1]...)
	}
	d.acc, d.n = 0, 0
}

// clipValue is the text of one clipboard write, from its base64 data: the
// standard alphabet, and the URL one when the data has "-" or "_". tee, when
// set, also gets the text of the standard alphabet.
type clipValue struct {
	std, url b64Stream
	ts, tu   *textHash
	tee      *textHash
}

func newClipValue() *clipValue {
	return &clipValue{url: b64Stream{url: true}, ts: newTextHash(), tu: newTextHash()}
}

func (v *clipValue) write(data []byte) {
	v.text(v.std.write(data))
	v.tu.write(v.url.write(data))
}

func (v *clipValue) text(b []byte) {
	v.ts.write(b)
	if v.tee != nil {
		v.tee.write(b)
	}
}

// end finishes the value, and returns the hashes to keep.
func (v *clipValue) end() [][32]byte {
	v.text(v.std.end())
	v.tu.write(v.url.end())
	var out [][32]byte
	if s, ok := v.ts.sum(); ok {
		out = append(out, s)
	}
	if u, ok := v.tu.sum(); ok && v.std.sawU {
		out = append(out, u)
	}
	return out
}
