package api

// OrderedJSONStrings and OrderedJSONFields are value cursors over a known
// source. They retain spans only. Ordering is decoded byte order.
type OrderedJSONStrings struct{ state projectedOrderedStrings }

func (p *JSONProjection) OrderedStrings(span JSONSpan) (OrderedJSONStrings, error) {
	state, err := p.orderedStrings(span)
	return OrderedJSONStrings{state: state}, err
}

func (i *OrderedJSONStrings) Next() (JSONSpan, bool, error) { return i.state.next() }

type OrderedJSONFields struct {
	c         jsonCursor
	end       int64
	previous  JSONSpan
	first     bool
	done      bool
	unordered bool // duplicate/order facts supplied by an actual same-store index
}

// OrderedFields is an opt-in stored-format rule. Ordinary JSON objects
// and public wire maps continue to use Object without this ordering rule.
func (p *JSONProjection) OrderedFields(span JSONSpan) (OrderedJSONFields, error) {
	i := OrderedJSONFields{c: p.cursor(span.Start), end: span.End, first: true}
	if err := p.validSpan(span); err != nil {
		return i, err
	}
	return i, i.c.expect('{')
}

func (i *OrderedJSONFields) Next() (JSONField, bool, error) {
	if i.done {
		return JSONField{}, false, nil
	}
	if err := i.c.space(); err != nil {
		return JSONField{}, false, err
	}
	b, err := i.c.peek()
	if err != nil {
		return JSONField{}, false, err
	}
	if b == '}' {
		i.c.pos++
		i.done = true
		return JSONField{}, false, i.c.ending(i.end)
	}
	if !i.first {
		if err := i.c.expect(','); err != nil {
			return JSONField{}, false, err
		}
		if err := i.c.space(); err != nil {
			return JSONField{}, false, err
		}
	}
	start := i.c.pos
	if err := i.c.stringValue(); err != nil {
		return JSONField{}, false, err
	}
	name := JSONSpan{Start: start, End: i.c.pos}
	if !i.first && !i.unordered {
		order, err := i.c.p.CompareStrings(i.previous, name)
		if err != nil || order >= 0 {
			if err != nil {
				return JSONField{}, false, err
			}
			return JSONField{}, false, ErrJSONProjection
		}
	}
	if err := i.c.space(); err != nil {
		return JSONField{}, false, err
	}
	if err := i.c.expect(':'); err != nil {
		return JSONField{}, false, err
	}
	if err := i.c.space(); err != nil {
		return JSONField{}, false, err
	}
	start = i.c.pos
	if err := i.c.value(); err != nil {
		return JSONField{}, false, err
	}
	i.first, i.previous = false, name
	return JSONField{Name: name, Value: JSONSpan{Start: start, End: i.c.pos}, Known: -1}, true, nil
}
