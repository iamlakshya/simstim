package api

import (
	"io"
	"regexp"
	"unicode/utf8"
)

// MatchRegexp applies the existing canonical regexp to decoded JSON text.
// Its reusable reader retains no string or payload body. Like the other
// projection visitors, it is synchronous and not safe for concurrent use.
func (p *JSONProjection) MatchRegexp(span JSONSpan, expression *regexp.Regexp) (bool, error) {
	if expression == nil {
		return false, ErrJSONProjection
	}
	iterator, err := p.stringIterator(span)
	if err != nil {
		return false, err
	}
	p.matchReader = jsonProjectionRuneReader{iterator: iterator}
	matched := expression.MatchReader(&p.matchReader)
	// A positive unanchored expression must not hide a malformed or
	// cancelled remainder. Canonical anchored validators already read EOF.
	if matched {
		for p.matchReader.err == nil {
			_, _, _ = p.matchReader.ReadRune()
		}
	}
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if err := p.matchReader.err; err != nil && err != io.EOF {
		return false, err
	}
	return matched, nil
}

type jsonProjectionRuneReader struct {
	iterator jsonStringIterator
	err      error
}

func (r *jsonProjectionRuneReader) ReadRune() (rune, int, error) {
	if r.err != nil {
		return 0, 0, r.err
	}
	value, err := r.iterator.next()
	if err != nil {
		r.err = err
		return 0, 0, err
	}
	return value, utf8.RuneLen(value), nil
}
