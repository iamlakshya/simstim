package api

// JSONValues is a pausable value cursor over one retained source array.
// It retains offsets only and shares JSONProjection's canonical value scanner.
type JSONValues struct {
	c     jsonCursor
	end   int64
	first bool
	done  bool
}

// Values opens a pausable array cursor. All values must be consumed to prove
// the complete array; a callback error never implies validation of its tail.
func (p *JSONProjection) Values(span JSONSpan) (JSONValues, error) {
	i := JSONValues{c: p.cursor(span.Start), end: span.End, first: true}
	if err := p.validSpan(span); err != nil {
		return i, err
	}
	return i, i.c.expect('[')
}

// Next returns the next encoded value span without materialising its body.
func (i *JSONValues) Next() (JSONSpan, bool, error) {
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
	if err := i.c.value(); err != nil {
		return JSONSpan{}, false, err
	}
	i.first = false
	return JSONSpan{Start: start, End: i.c.pos}, true, nil
}
