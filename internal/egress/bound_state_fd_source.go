package egress

import (
	"context"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func fdSpan(span api.JSONSpan) tomlinspect.Span {
	return tomlinspect.Span{Start: span.Start, End: span.End}
}
func jsonSpan(span tomlinspect.Span) api.JSONSpan {
	return api.JSONSpan{Start: span.Start, End: span.End}
}

// The semantic owner holds one existing inspection and its numeric records.
// No decoded name, header, stand-in, alphabet or historical state is retained.
type fdPublicState struct {
	ctx                          context.Context
	p                            *api.JSONProjection
	h                            *tomlinspect.Inspection
	charge                       tomlinspect.StateCharge
	region                       *tomlinspect.StateRegion
	schema                       PublicStateProjectionSchema
	shapes                       savedShapeJSONSchema
	sentinels, identities, hosts tomlinspect.Tree
	duplicateMap                 bool
}

type fdStateFields struct{ version, intent, generation, primary, minted, retired api.JSONSpan }
type fdIntentFields struct{ version, modes, binds api.JSONSpan }

func fdFields(p *api.JSONProjection, span api.JSONSpan, known []string, visit func(string, api.JSONSpan) error) error {
	return projectedOpaqueError(p.Object(span, known, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		return visit(known[field.Known], field.Value)
	}))
}

func (s *fdPublicState) stateFields(span api.JSONSpan) (fdStateFields, error) {
	var fields fdStateFields
	err := fdFields(s.p, span, s.schema.state, func(key string, value api.JSONSpan) error {
		switch key {
		case "version":
			fields.version = value
		case "intent":
			fields.intent = value
		case "generation":
			fields.generation = value
		case "primary":
			fields.primary = value
		case "minted":
			fields.minted = value
		case "retired":
			fields.retired = value
		}
		return nil
	})
	return fields, err
}

func (s *fdPublicState) intentFields(span api.JSONSpan) (fdIntentFields, error) {
	var fields fdIntentFields
	err := fdFields(s.p, span, s.schema.intent, func(key string, value api.JSONSpan) error {
		switch key {
		case "version":
			fields.version = value
		case "modes":
			fields.modes = value
		case "binds":
			fields.binds = value
		}
		return nil
	})
	return fields, err
}

func fdStringEqual(p *api.JSONProjection, span api.JSONSpan, want string) (bool, error) {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return null && want == "", err
	}
	return p.StringEqual(span, want)
}

func fdBool(p *api.JSONProjection, span api.JSONSpan) (bool, error) {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return false, err
	}
	value, err := p.Literal(span, "true")
	if err != nil || value {
		return value, err
	}
	value, err = p.Literal(span, "false")
	if err != nil {
		return false, err
	}
	if !value {
		return false, errProjectedStateOpaque
	}
	return false, nil
}

func fdNonce(p *api.JSONProjection, span api.JSONSpan) error {
	var value [32]byte
	used := 0
	err := walkProjectedString(p, span, func(r rune) error {
		if used == len(value) || !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return invalidPublicSessionState()
		}
		value[used] = byte(r)
		used++
		return nil
	})
	if err != nil {
		return err
	}
	if used != len(value) {
		return invalidPublicSessionState()
	}
	return nil
}

func fdName(p *api.JSONProjection, span api.JSONSpan) error {
	first := true
	prefix := true
	position := 0
	err := p.WalkString(span, func(r rune) error {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || !first && r >= '0' && r <= '9') {
			return invalidPublicSessionState()
		}
		first = false
		if position < len(brand.EnvPrefix) {
			prefix = prefix && r == rune(brand.EnvPrefix[position])
			position++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if first || prefix && position == len(brand.EnvPrefix) {
		return invalidPublicSessionState()
	}
	for _, reserved := range []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION"} {
		equal, err := p.StringEqual(span, reserved)
		if err != nil {
			return err
		}
		if equal {
			return invalidPublicSessionState()
		}
	}
	return nil
}

func fdSavedPrefix(p *api.JSONProjection, name, prefix api.JSONSpan) (string, error) {
	if _, err := agents.GlobalSecretDefaults(); err != nil {
		return "", err
	}
	empty, err := fdStringEqual(p, prefix, "")
	if err != nil || empty {
		return "", err
	}
	for _, agent := range agents.Table {
		for _, binding := range agent.SecretBindings() {
			equal, err := p.StringEqual(name, binding.Env)
			if err != nil {
				return "", err
			}
			if !equal {
				continue
			}
			equal, err = p.StringEqual(prefix, binding.Prefix)
			if err != nil {
				return "", err
			}
			if equal {
				return binding.Prefix, nil
			}
		}
	}
	return "", invalidPublicSessionState()
}

func fdWriteString(p *api.JSONProjection, span api.JSONSpan, write func([]byte) (int, error)) error {
	var scratch [utf8.UTFMax]byte
	return walkProjectedString(p, span, func(r rune) error {
		part := utf8.AppendRune(scratch[:0], r)
		_, err := write(part)
		return err
	})
}

func fdCompareStrings(p *api.JSONProjection, a, b api.JSONSpan, fold bool) (int, error) {
	x, err := p.StringRunes(a)
	if err != nil {
		return 0, err
	}
	y, err := p.StringRunes(b)
	if err != nil {
		return 0, err
	}
	for {
		left, lok, err := x.Next()
		if err != nil {
			return 0, err
		}
		right, rok, err := y.Next()
		if err != nil {
			return 0, err
		}
		if !lok || !rok {
			if lok {
				return 1, nil
			}
			if rok {
				return -1, nil
			}
			return 0, nil
		}
		if fold {
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if right >= 'A' && right <= 'Z' {
				right += 'a' - 'A'
			}
		}
		if left < right {
			return -1, nil
		}
		if left > right {
			return 1, nil
		}
	}
}

func fdSameOptionalString(p *api.JSONProjection, a, b api.JSONSpan) (bool, error) {
	an, err := projectedNull(p, a)
	if err != nil {
		return false, err
	}
	bn, err := projectedNull(p, b)
	if err != nil {
		return false, err
	}
	if an {
		return fdStringEqual(p, b, "")
	}
	if bn {
		return fdStringEqual(p, a, "")
	}
	order, err := p.CompareStrings(a, b)
	return order == 0, err
}

func fdFormatKind(kind string) uint8 {
	switch kind {
	case "raw":
		return 1
	case "bearer":
		return 2
	case "basic":
		return 3
	}
	return 0
}
func fdFormatName(kind uint8) string {
	switch kind {
	case 1:
		return "raw"
	case 2:
		return "bearer"
	case 3:
		return "basic"
	}
	return ""
}
