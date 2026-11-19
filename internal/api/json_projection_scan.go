package api

import "io"

const (
	jsonProjectionDepth = 10000 // Existing encoding/json syntax boundary.
	jsonObjectFirst     = iota + 1
	jsonObjectName
	jsonObjectValue
	jsonObjectAfter
	jsonArrayFirst
	jsonArrayValue
	jsonArrayAfter
)

// value has an explicit bounded grammar stack. Unknown strings are checked
// byte by byte, including escapes, without constructing a token string.
func (c *jsonCursor) value() error {
	var stack [jsonProjectionDepth]byte
	depth := 0
	needValue := true
	for {
		if err := c.space(); err != nil {
			return err
		}
		if needValue {
			b, err := c.peek()
			if err != nil {
				return err
			}
			switch b {
			case '{', '[':
				if depth == len(stack) {
					return ErrJSONProjection
				}
				c.pos++
				stack[depth] = jsonObjectFirst
				if b == '[' {
					stack[depth] = jsonArrayFirst
				}
				depth++
			case '"':
				if err := c.stringValue(); err != nil {
					return err
				}
			case 't', 'f', 'n':
				word := "null"
				if b == 't' {
					word = "true"
				} else if b == 'f' {
					word = "false"
				}
				for i := range len(word) {
					if err := c.expect(word[i]); err != nil {
						return err
					}
				}
			default:
				if err := c.number(); err != nil {
					return err
				}
			}
			needValue = false
			if depth == 0 {
				return nil
			}
			continue
		}
		b, err := c.peek()
		if err != nil {
			return err
		}
		switch stack[depth-1] {
		case jsonObjectFirst, jsonObjectName:
			if b == '}' && stack[depth-1] == jsonObjectFirst {
				c.pos++
				depth--
				break
			}
			if err := c.stringValue(); err != nil {
				return err
			}
			if err := c.space(); err != nil {
				return err
			}
			if err := c.expect(':'); err != nil {
				return err
			}
			stack[depth-1] = jsonObjectValue
		case jsonObjectValue:
			stack[depth-1], needValue = jsonObjectAfter, true
		case jsonObjectAfter:
			c.pos++
			if b == ',' {
				stack[depth-1] = jsonObjectName
			} else if b == '}' {
				depth--
			} else {
				return ErrJSONProjection
			}
		case jsonArrayFirst, jsonArrayValue:
			if b == ']' && stack[depth-1] == jsonArrayFirst {
				c.pos++
				depth--
				break
			}
			stack[depth-1], needValue = jsonArrayAfter, true
		case jsonArrayAfter:
			c.pos++
			if b == ',' {
				stack[depth-1] = jsonArrayValue
			} else if b == ']' {
				depth--
			} else {
				return ErrJSONProjection
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func (c *jsonCursor) stringValue() error {
	if err := c.expect('"'); err != nil {
		return err
	}
	for {
		b, err := c.stringByteRun()
		if err != nil {
			return err
		}
		switch {
		case b == '"':
			return nil
		case b == '\\':
			b, err = c.take()
			if err != nil {
				return err
			}
			if b == 'u' {
				for range 4 {
					b, err := c.take()
					if err != nil {
						return err
					}
					if hexJSON(b) < 0 {
						return ErrJSONProjection
					}
				}
			} else if b != '"' && b != '\\' && b != '/' && b != 'b' && b != 'f' && b != 'n' && b != 'r' && b != 't' {
				return ErrJSONProjection
			}
		case b < 0x20:
			return ErrJSONProjection
		}
	}
}

func hexJSON(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func (c *jsonCursor) digits() error {
	b, err := c.peek()
	if err != nil || b < '0' || b > '9' {
		if err != nil {
			return err
		}
		return ErrJSONProjection
	}
	for err == nil && b >= '0' && b <= '9' {
		c.pos++
		b, err = c.peek()
	}
	if err == io.EOF {
		return nil
	}
	return err
}

func (c *jsonCursor) number() error {
	b, err := c.peek()
	if err != nil {
		return err
	}
	if b == '-' {
		c.pos++
		b, err = c.peek()
		if err != nil {
			return err
		}
	}
	if b == '0' {
		c.pos++
	} else if b >= '1' && b <= '9' {
		if err := c.digits(); err != nil {
			return err
		}
	} else {
		return ErrJSONProjection
	}
	if b, err = c.peek(); err == nil && b == '.' {
		c.pos++
		if err := c.digits(); err != nil {
			return err
		}
	}
	if b, err = c.peek(); err == nil && (b == 'e' || b == 'E') {
		c.pos++
		if b, err = c.peek(); err == nil && (b == '+' || b == '-') {
			c.pos++
		}
		if err := c.digits(); err != nil {
			return err
		}
	}
	if err == io.EOF {
		return nil
	}
	return err
}
