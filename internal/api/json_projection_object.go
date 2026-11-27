package api

import (
	"io"
	"unicode"
	"unicode/utf8"
)

type jsonSeenName struct {
	span JSONSpan
	hash [32]byte
}

// Object's fixed table is assistance, not an input cap. Matching hashes
// always receive an exact decoded comparison. Once full, sibling rescans
// preserve exact semantics with explicitly unproven quadratic cost.
func (p *JSONProjection) Object(span JSONSpan, known []string, visit func(JSONField) error) error {
	return p.object(span, known, nil, visit)
}

// object shares the grammar and alias checks with a materialising caller
// that already owns exact decoded names. Agent readers leave checkName nil
// and retain only the fixed assistance table and exact bounded rescans.
func (p *JSONProjection) object(span JSONSpan, known []string, checkName func(JSONSpan) error, visit func(JSONField) error) error {
	if err := p.validSpan(span); err != nil {
		return err
	}
	c := p.cursor(span.Start)
	if err := c.expect('{'); err != nil {
		return err
	}
	var seen [64]jsonSeenName
	count := 0
	var previous JSONSpan
	ordered := true
	if err := c.space(); err != nil {
		return err
	}
	if b, _ := c.peek(); b == '}' {
		c.pos++
		return c.ending(span.End)
	}
	for {
		if err := c.space(); err != nil {
			return err
		}
		name := JSONSpan{Start: c.pos}
		if err := c.stringValue(); err != nil {
			return err
		}
		name.End = c.pos
		ascending := false
		if checkName != nil {
			if err := checkName(name); err != nil {
				return err
			}
		} else if previous.End != 0 {
			order, err := p.CompareStrings(previous, name)
			if err != nil {
				return err
			}
			if order == 0 {
				return ErrJSONProjection
			}
			ascending = ordered && order < 0
			if order > 0 {
				ordered = false
			}
		}
		if checkName == nil && count < len(seen) {
			digest, err := p.nameHash(name)
			if err != nil {
				return err
			}
			for j := 0; !ascending && j < count; j++ {
				if digest == seen[j].hash {
					equal, err := p.CompareStrings(name, seen[j].span)
					if err != nil || equal == 0 {
						if err != nil {
							return err
						}
						return ErrJSONProjection
					}
				}
			}
			seen[count] = jsonSeenName{span: name, hash: digest}
			count++
		} else if checkName == nil && !ascending {
			if err := p.uniqueBefore(span.Start, name); err != nil {
				return err
			}
		}
		previous = name
		index, err := p.knownName(name, known)
		if err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		if err := c.expect(':'); err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		value := JSONSpan{Start: c.pos}
		if err := c.value(); err != nil {
			return err
		}
		value.End = c.pos
		if err := visit(JSONField{Name: name, Value: value, Known: index}); err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		b, err := c.take()
		if err != nil {
			return err
		}
		if b == '}' {
			return c.ending(span.End)
		}
		if b != ',' {
			return ErrJSONProjection
		}
	}
}

func (p *JSONProjection) uniqueBefore(objectStart int64, name JSONSpan) error {
	c := p.cursor(objectStart + 1)
	for {
		if err := c.space(); err != nil {
			return err
		}
		if c.pos == name.Start {
			return nil
		}
		if c.pos > name.Start {
			return ErrJSONProjection
		}
		prior := JSONSpan{Start: c.pos}
		if err := c.stringValue(); err != nil {
			return err
		}
		prior.End = c.pos
		order, err := p.CompareStrings(prior, name)
		if err != nil || order == 0 {
			if err != nil {
				return err
			}
			return ErrJSONProjection
		}
		if err := c.space(); err != nil {
			return err
		}
		if err := c.expect(':'); err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		if err := c.value(); err != nil {
			return err
		}
		if err := c.space(); err != nil {
			return err
		}
		if err := c.expect(','); err != nil {
			return err
		}
	}
}

func (p *JSONProjection) nameHash(span JSONSpan) ([32]byte, error) {
	if p.testNameHash != nil {
		return p.testNameHash(span)
	}
	p.nameDigest.Reset()
	err := p.WalkString(span, func(r rune) error {
		n := utf8.EncodeRune(p.nameRune[:], r)
		_, err := p.nameDigest.Write(p.nameRune[:n])
		return err
	})
	p.nameDigest.Sum(p.nameSum[:0])
	return p.nameSum, err
}

func foldEqualRune(a, b rune) bool {
	if a == b {
		return true
	}
	for next := unicode.SimpleFold(a); next != a; next = unicode.SimpleFold(next) {
		if next == b {
			return true
		}
	}
	return false
}

func (p *JSONProjection) knownName(span JSONSpan, known []string) (int, error) {
	matched := -1
	for index, name := range known {
		iterator, err := p.stringIterator(span)
		if err != nil {
			return -1, err
		}
		exact, folded := true, true
		for _, expected := range name {
			r, err := iterator.next()
			if err != nil {
				if err != io.EOF {
					return -1, err
				}
				exact, folded = false, false
				break
			}
			exact = exact && r == expected
			folded = folded && foldEqualRune(r, expected)
		}
		if _, err := iterator.next(); err == nil {
			exact, folded = false, false
		} else if err != io.EOF {
			return -1, err
		}
		if folded && !exact {
			return -1, ErrJSONProjection
		}
		if exact {
			matched = index
		}
	}
	return matched, nil
}
