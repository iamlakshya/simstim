package api

import (
	"context"
	"encoding/json"
	"strings"
)

// ProjectedJSONObjectFields is the materialising adapter for differential
// parity with JSONObjectFields. Agent admission never calls this adapter.
// The existing JSONObjectFields error wrapper remains an integration seam.
func ProjectedJSONObjectFields(ctx context.Context, raw []byte, known ...string) (fields map[string]json.RawMessage, err error) {
	defer func() {
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ErrJSONObject
			}
		}
	}()
	p, err := NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return nil, err
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return nil, err
	}
	fields = map[string]json.RawMessage{}
	var decodedName string
	checkName := func(span JSONSpan) error {
		var name strings.Builder
		if err := p.WalkString(span, func(r rune) error { name.WriteRune(r); return nil }); err != nil {
			return err
		}
		decodedName = name.String()
		if _, duplicate := fields[decodedName]; duplicate {
			return ErrJSONObject
		}
		return nil
	}
	err = p.object(root, known, checkName, func(field JSONField) error {
		fields[decodedName] = append(json.RawMessage(nil), raw[field.Value.Start:field.Value.End]...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fields, nil
}
