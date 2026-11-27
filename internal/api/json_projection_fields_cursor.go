package api

// JSONFields visits source fields in their original order through the same
// cursor as OrderedFields. It validates grammar only. Its actual owner must
// establish duplicate/name relations in its admitted numeric index before
// treating a DTO as supported. It retains no decoded names or name table.
type JSONFields struct{ cursor OrderedJSONFields }

func (p *JSONProjection) Fields(span JSONSpan) (JSONFields, error) {
	cursor, err := p.OrderedFields(span)
	cursor.unordered = true
	return JSONFields{cursor: cursor}, err
}

func (c *JSONFields) Next() (JSONField, bool, error) {
	if c == nil || c.cursor.c.p == nil {
		return JSONField{}, false, ErrJSONProjection
	}
	return c.cursor.Next()
}
