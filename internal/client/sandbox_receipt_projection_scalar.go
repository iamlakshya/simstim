package client

import (
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
)

func receiptProjectionString(p *api.JSONProjection, span api.JSONSpan) (nonempty bool, err error) {
	if span.End == 0 {
		return false, nil
	}
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return false, err
	}
	err = p.WalkString(span, func(rune) error { nonempty = true; return nil })
	return nonempty, err
}

func receiptProjectionInteger(p *api.JSONProjection, span api.JSONSpan, unsigned bool) (int64, error) {
	if span.End == 0 {
		return 0, nil
	}
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return 0, err
	}
	var bytes [21]byte
	n := 0
	err = p.WalkEncoded(span, func(part []byte) error {
		if len(part) > len(bytes)-n {
			return badSandboxState()
		}
		n += copy(bytes[n:], part)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if unsigned {
		value, err := strconv.ParseUint(string(bytes[:n]), 10, 32)
		return int64(value), err
	}
	return strconv.ParseInt(string(bytes[:n]), 10, 64)
}

func receiptProjectionBoolean(p *api.JSONProjection, span api.JSONSpan) (bool, error) {
	if span.End == 0 {
		return false, nil
	}
	return projectedOptionalBoolean(p, span)
}

func receiptProjectionHexValue(p *api.JSONProjection, span api.JSONSpan, size int) (valid bool, err error) {
	valid = true
	n := 0
	err = p.WalkString(span, func(r rune) error {
		if n >= size || !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			valid = false
		}
		if n <= size {
			n++
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return valid && n == size, nil
}
func receiptProjectionHex(p *api.JSONProjection, span api.JSONSpan, size int) error {
	valid, err := receiptProjectionHexValue(p, span, size)
	if err != nil {
		return err
	}
	if !valid {
		return sandboxReceiptError()
	}
	return nil
}

func receiptProjectionNames(p *api.JSONProjection, span api.JSONSpan, semantic bool) error {
	if span.End == 0 {
		if semantic {
			return sandboxReceiptError()
		}
		return nil
	}
	null, err := p.Literal(span, "null")
	if err != nil {
		return err
	}
	if null {
		if semantic {
			return sandboxReceiptError()
		}
		return nil
	}
	var previous api.JSONSpan
	return p.Array(span, func(name api.JSONSpan) error {
		if !semantic {
			_, err := receiptProjectionString(p, name)
			if err != nil {
				return err
			}
			return nil // encoding/json accepts null string elements as empty.

		}
		if err := projectedSecretName(p, name); err != nil {
			return err
		}
		reserved, err := p.StringEqual(name, "SSH_AUTH_SOCK")
		if err != nil {
			return err
		}
		if reserved {
			return sandboxReceiptError()
		}
		if previous.End != 0 {
			order, err := p.CompareStrings(previous, name)
			if err != nil {
				return err
			}
			if order >= 0 {
				return sandboxReceiptError()
			}
		}
		previous = name
		return nil
	})
}

// This projects the SAME historical Source DTO's scalar metadata. It never
// parses a native identity or constructs a Reference/checked Source. The root
// identity is only tested for the exact nonempty predicate in Reference.Known.
func projectReceiptSource(p *api.JSONProjection, span api.JSONSpan) (future bool, err error) {
	var fields [5]api.JSONSpan
	err = p.Object(span, hostStateSourceWitnessFields, func(f api.JSONField) error {
		if f.Known < 0 {
			future = true
		} else {
			fields[f.Known] = f.Value
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if fields[0].End == 0 {
		return false, badSandboxState()
	}
	version, err := receiptProjectionInteger(p, fields[0], false)
	if err != nil {
		return false, err
	}
	if version != 1 {
		return true, nil
	}
	exists, err := receiptProjectionBoolean(p, fields[1])
	if err != nil {
		return false, err
	}
	_, err = receiptProjectionString(p, fields[2])
	if err != nil {
		return false, err
	}
	path, err := receiptProjectionString(p, fields[3])
	if err != nil {
		return false, err
	}
	var root [6]api.JSONSpan
	err = p.Object(fields[4], hostStateSourceRootFields, func(f api.JSONField) error {
		if f.Known < 0 {
			future = true
		} else {
			root[f.Known] = f.Value
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	rv, err := receiptProjectionInteger(p, root[0], false)
	if err != nil {
		return false, err
	}
	identity, err := receiptProjectionString(p, root[1])
	if err != nil {
		return false, err
	}
	mode, err := receiptProjectionInteger(p, root[2], true)
	if err != nil {
		return false, err
	}
	size, err := receiptProjectionInteger(p, root[3], false)
	if err != nil {
		return false, err
	}
	modified := false
	if root[4].End != 0 {
		value, e := readProjectedTime(p, root[4])
		if e != nil {
			return false, e
		}
		modified = !value.instant.IsZero()
	}
	directory, err := receiptProjectionBoolean(p, root[5])
	if err != nil {
		return false, err
	}
	if !exists || !path || rv != 1 || !identity || mode&^int64(0777) != 0 || size < 0 || !modified || !directory {
		future = true
	}
	validGeneration, err := receiptProjectionHexValue(p, fields[2], 32)
	if err != nil {
		return false, err
	}
	if !validGeneration {
		future = true
	}
	return future, nil
}
