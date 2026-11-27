package api

import "io"

// JSONRunes borrows the same source and decoder as WalkString. It retains
// only its current encoded offset, never decoded source text. The owner must
// keep the source and its inspection epoch unchanged until iteration ends.
type JSONRunes struct {
	iterator jsonStringIterator
	done     bool
}

func (p *JSONProjection) StringRunes(span JSONSpan) (JSONRunes, error) {
	iterator, err := p.stringIterator(span)
	return JSONRunes{iterator: iterator}, err
}

func (r *JSONRunes) Next() (rune, bool, error) {
	if r == nil || r.iterator.c.p == nil {
		return 0, false, ErrJSONProjection
	}
	if r.done {
		return 0, false, nil
	}
	value, err := r.iterator.next()
	if err == io.EOF {
		r.done = true
		return 0, false, nil
	}
	return value, err == nil, err
}
