package api

// OrderedStringArraySubset compares two strictly ordered string arrays
// using source cursors. It retains no names, index or array of spans.
func (p *JSONProjection) OrderedStringArraySubset(subset, superset JSONSpan) (bool, error) {
	a, err := p.orderedStrings(subset)
	if err != nil {
		return false, err
	}
	b, err := p.orderedStrings(superset)
	if err != nil {
		return false, err
	}
	x, xok, err := a.next()
	if err != nil {
		return false, err
	}
	y, yok, err := b.next()
	if err != nil {
		return false, err
	}
	contained := true
	for xok && yok {
		order, err := p.CompareStrings(x, y)
		if err != nil {
			return false, err
		}
		if order < 0 {
			contained = false
		}
		if order <= 0 {
			x, xok, err = a.next()
		} else {
			y, yok, err = b.next()
		}
		if err != nil {
			return false, err
		}
	}
	if xok {
		contained = false
	}
	// A mismatch does not skip malformed tails or ordering errors.
	for xok {
		_, xok, err = a.next()
		if err != nil {
			return false, err
		}
	}
	for yok {
		_, yok, err = b.next()
		if err != nil {
			return false, err
		}
	}
	return contained, nil
}

type projectedOrderedStrings struct {
	c        jsonCursor
	end      int64
	previous JSONSpan
	first    bool
	done     bool
}

func (p *JSONProjection) orderedStrings(span JSONSpan) (projectedOrderedStrings, error) {
	i := projectedOrderedStrings{c: p.cursor(span.Start), end: span.End, first: true}
	if err := p.validSpan(span); err != nil {
		return i, err
	}
	return i, i.c.expect('[')
}

func (i *projectedOrderedStrings) next() (JSONSpan, bool, error) {
	if i.done {
		return JSONSpan{}, false, nil
	}
	if err := i.c.space(); err != nil {
		return JSONSpan{}, false, err
	}
	b, err := i.c.peek()
	if err != nil {
		return JSONSpan{}, false, err
	}
	if b == ']' {
		i.c.pos++
		i.done = true
		return JSONSpan{}, false, i.c.ending(i.end)
	}
	if !i.first {
		if err := i.c.expect(','); err != nil {
			return JSONSpan{}, false, err
		}
		if err := i.c.space(); err != nil {
			return JSONSpan{}, false, err
		}
	}
	start := i.c.pos
	if err := i.c.stringValue(); err != nil {
		return JSONSpan{}, false, err
	}
	value := JSONSpan{Start: start, End: i.c.pos}
	if !i.first {
		order, err := i.c.p.CompareStrings(i.previous, value)
		if err != nil || order >= 0 {
			if err != nil {
				return JSONSpan{}, false, err
			}
			return JSONSpan{}, false, ErrJSONProjection
		}
	}
	i.first, i.previous = false, value
	return value, true, nil
}
