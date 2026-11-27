package api

import (
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

type jsonStringIterator struct {
	c   jsonCursor
	end int64
}

func (p *JSONProjection) stringIterator(span JSONSpan) (jsonStringIterator, error) {
	if err := p.validSpan(span); err != nil {
		return jsonStringIterator{}, err
	}
	c := p.cursor(span.Start)
	if err := c.expect('"'); err != nil {
		return jsonStringIterator{}, err
	}
	last := p.cursor(span.End - 1)
	if err := last.expect('"'); err != nil {
		return jsonStringIterator{}, err
	}
	return jsonStringIterator{c: c, end: span.End - 1}, nil
}

func (p *JSONProjection) WalkString(span JSONSpan, visit func(rune) error) error {
	return p.WalkStringSpans(span, func(r rune, _ JSONSpan) error { return visit(r) })
}

func (p *JSONProjection) StringEqual(span JSONSpan, want string) (bool, error) {
	iterator, err := p.stringIterator(span)
	if err != nil {
		return false, err
	}
	// JSON decoding always produces valid UTF-8, including replacement
	// runes. A Go string containing invalid bytes cannot equal that result.
	if !utf8.ValidString(want) {
		return false, nil
	}
	for _, expected := range want {
		r, err := iterator.next()
		if err != nil {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		if r != expected {
			return false, nil
		}
	}
	_, err = iterator.next()
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

// CompareStrings compares decoded string bytes. Valid UTF-8 rune order
// equals byte order; malformed input follows encoding/json replacement.
func (p *JSONProjection) CompareStrings(a, b JSONSpan) (int, error) {
	left, err := p.stringIterator(a)
	if err != nil {
		return 0, err
	}
	right, err := p.stringIterator(b)
	if err != nil {
		return 0, err
	}
	for {
		x, xerr := left.next()
		y, yerr := right.next()
		if xerr != nil && xerr != io.EOF {
			return 0, xerr
		}
		if yerr != nil && yerr != io.EOF {
			return 0, yerr
		}
		if xerr == io.EOF && yerr == io.EOF {
			return 0, nil
		}
		if xerr == io.EOF || x < y && yerr == nil {
			return -1, nil
		}
		if yerr == io.EOF || x > y {
			return 1, nil
		}
	}
}

func (i *jsonStringIterator) next() (rune, error) {
	if err := i.c.p.ctx.Err(); err != nil {
		return 0, err
	}
	if i.c.pos == i.end {
		return 0, io.EOF
	}
	if i.c.pos > i.end {
		return 0, ErrJSONProjection
	}
	b, err := i.c.take()
	if err != nil {
		return 0, err
	}
	if b == '\\' {
		return i.escape()
	}
	if b < 0x20 || b == '"' {
		return 0, ErrJSONProjection
	}
	if b < utf8.RuneSelf {
		return rune(b), nil
	}
	start := i.c.pos - 1
	var encoded [utf8.UTFMax]byte
	encoded[0] = b
	n := int(min(int64(len(encoded)), i.end-start))
	for j := 1; j < n; j++ {
		encoded[j], err = i.c.take()
		if err != nil {
			return 0, err
		}
	}
	r, size := utf8.DecodeRune(encoded[:n])
	i.c.pos = start + int64(size)
	return r, nil
}

func (i *jsonStringIterator) escape() (rune, error) {
	b, err := i.c.take()
	if err != nil {
		return 0, err
	}
	switch b {
	case '"', '\\', '/':
		return rune(b), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r, err := i.hexRune()
		if err != nil || !utf16.IsSurrogate(r) {
			return r, err
		}
		resume := i.c.pos
		if i.end-resume >= 6 {
			first := i.c.expect('\\')
			if first != nil && first != ErrJSONProjection {
				return 0, first
			}
			if first == nil {
				second := i.c.expect('u')
				if second != nil && second != ErrJSONProjection {
					return 0, second
				}
				if second == nil {
					next, err := i.hexRune()
					if err != nil {
						return 0, err
					}
					if combined := utf16.DecodeRune(r, next); combined != utf8.RuneError {
						return combined, nil
					}
				}
			}
		}
		if err := i.c.p.ctx.Err(); err != nil {
			return 0, err
		}
		i.c.pos = resume
		return utf8.RuneError, nil
	}
	return 0, ErrJSONProjection
}

func (i *jsonStringIterator) hexRune() (rune, error) {
	var r rune
	for range 4 {
		if i.c.pos >= i.end {
			return 0, ErrJSONProjection
		}
		b, err := i.c.take()
		if err != nil {
			return 0, err
		}
		x := hexJSON(b)
		if x < 0 {
			return 0, ErrJSONProjection
		}
		r = r*16 + rune(x)
	}
	return r, nil
}
