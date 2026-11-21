package egress

import "github.com/iamlakshya/simstim/internal/api"

// ErrUnsupportedPublicState identifies only an opaque DTO representation.
// Validate emits it before any charged core operation. A caller may preserve
// an unrelated future receipt; it must refuse a selected one. Semantic errors
// and source/cancellation failures never gain this classification.
var ErrUnsupportedPublicState = errProjectedStateOpaque

const (
	fdDTOState byte = iota
	fdDTOIntent
	fdDTOBinding
	fdDTOPrimary
	fdDTOMinted
	fdDTORetired
	fdDTOShape
	fdDTOSlot
	fdDTOTrustedUse
)

func (s *fdPublicState) typeString(span api.JSONSpan) error {
	return walkProjectedString(s.p, span, func(rune) error { return nil })
}
func (s *fdPublicState) typeInt(span api.JSONSpan) error {
	_, err := projectedNativeInt(s.p, span)
	return err
}
func (s *fdPublicState) typeBool(span api.JSONSpan) error { _, err := fdBool(s.p, span); return err }

func (s *fdPublicState) typeArray(span api.JSONSpan, visit func(api.JSONSpan) error) error {
	null, err := projectedNull(s.p, span)
	if err != nil || null {
		return err
	}
	return projectedOpaqueError(s.p.Array(span, visit))
}

func (s *fdPublicState) typeMap(span api.JSONSpan, visit func(api.JSONSpan) error) error {
	null, err := projectedNull(s.p, span)
	if err != nil || null {
		return err
	}
	return projectedStoredMap(s.p, span, func(field api.JSONField) error { return visit(field.Value) })
}

// classifyDTO mirrors the finite DTO descriptors and encoding/json's
// null-as-zero rules. It validates all nested known fields and scalar types,
// without imposing stored key order or interpreting a credential's meaning.
// Arbitrary map duplicates are decided by the SAME charged grouped index;
// a duplicate is a boolean fact and the region retires before opaque returns.
// The ONE projection owns JSON grammar, escapes and duplicate-field checks.
func (s *fdPublicState) classifyDTO(span api.JSONSpan, kind byte) error {
	null, err := projectedNull(s.p, span)
	if err != nil || null {
		return err
	}
	var known []string
	switch kind {
	case fdDTOState:
		known = s.schema.state
	case fdDTOIntent:
		known = s.schema.intent
	case fdDTOBinding:
		known = s.schema.binding
	case fdDTOPrimary:
		known = s.schema.primary
	case fdDTOMinted:
		known = s.schema.minted
	case fdDTORetired:
		known = s.schema.retired
	case fdDTOShape:
		known = s.shapes.shape
	case fdDTOSlot:
		known = s.shapes.slot
	case fdDTOTrustedUse:
		known = s.schema.trusted
	default:
		return errProjectedStateOpaque
	}
	return fdFields(s.p, span, known, func(key string, value api.JSONSpan) error {
		switch kind {
		case fdDTOState:
			switch key {
			case "version":
				return s.typeInt(value)
			case "generation":
				return s.typeString(value)
			case "intent":
				return s.classifyDTO(value, fdDTOIntent)
			case "primary":
				return s.typeMap(value, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTOPrimary) })
			case "minted":
				return s.typeMap(value, func(v api.JSONSpan) error {
					return s.typeArray(v, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTOMinted) })
				})
			case "retired":
				return s.typeMap(value, func(v api.JSONSpan) error {
					return s.typeArray(v, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTORetired) })
				})
			}
		case fdDTOIntent:
			switch key {
			case "version":
				return s.typeInt(value)
			case "modes":
				return s.typeMap(value, s.typeString)
			case "binds":
				return s.typeMap(value, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTOBinding) })
			case "trusted_uses":
				return s.typeMap(value, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTOTrustedUse) })
			}
		case fdDTOTrustedUse:
			if key == "bind" {
				return s.classifyDTO(value, fdDTOBinding)
			}
			if key == "v" {
				return s.typeInt(value)
			}
			return s.typeString(value)
		case fdDTOBinding:
			if key == "hosts" {
				return s.typeArray(value, s.typeString)
			}
			return s.typeString(value)
		case fdDTOPrimary:
			switch key {
			case "shape":
				return s.classifyDTO(value, fdDTOShape)
			case "armed":
				return s.typeBool(value)
			default:
				return s.typeString(value)
			}
		case fdDTOMinted:
			if key == "shape" {
				return s.classifyDTO(value, fdDTOShape)
			}
			return s.typeString(value)
		case fdDTORetired:
			switch key {
			case "binding":
				return s.classifyDTO(value, fdDTOBinding)
			case "shape":
				return s.classifyDTO(value, fdDTOShape)
			default:
				return s.typeString(value)
			}
		case fdDTOShape:
			switch key {
			case "version":
				return s.typeInt(value)
			case "slots":
				return s.typeArray(value, func(v api.JSONSpan) error { return s.classifyDTO(v, fdDTOSlot) })
			default:
				return s.typeString(value)
			}
		case fdDTOSlot:
			if key == "width" || key == "count" {
				return s.typeInt(value)
			}
			return s.typeString(value)
		}
		return errProjectedStateOpaque
	})
}
