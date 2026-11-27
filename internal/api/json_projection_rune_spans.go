package api

import "io"

// WalkStringSpans decodes through the SAME string iterator as WalkString.
// The span is exactly the encoded source consumed for that decoded rune,
// including both escapes of a surrogate pair. Source must remain unchanged.
func (p *JSONProjection) WalkStringSpans(span JSONSpan, visit func(rune, JSONSpan) error) error {
	iterator, err := p.stringIterator(span)
	if err != nil {
		return err
	}
	for {
		start := iterator.c.pos
		r, err := iterator.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(r, JSONSpan{Start: start, End: iterator.c.pos}); err != nil {
			return err
		}
	}
}

// WalkStringEncodedWindow replays an already validated encoded rune window.
// Its caller must derive BOTH endpoints from a successful complete
// WalkStringSpans on this same unchanged source and retain them only through
// an owner-checked metadata record. This method validates no skipped prefix
// or suffix and grants no authority. It shares the original escape iterator;
// it does not rescan unbounded validated trim padding or copy source text.
func (p *JSONProjection) WalkStringEncodedWindow(whole, window JSONSpan, visit func(rune) error) error {
	if _, err := p.stringIterator(whole); err != nil {
		return err
	}
	if window.Start < whole.Start+1 || window.End < window.Start || window.End > whole.End-1 {
		return ErrJSONProjection
	}
	iterator := jsonStringIterator{c: p.cursor(window.Start), end: window.End}
	for {
		r, err := iterator.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(r); err != nil {
			return err
		}
	}
}
