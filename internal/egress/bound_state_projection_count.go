package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// PublicStateCountSink is the active SAME-store StateCounter callback. These
// operations count actual visited source records. They grant no authority.
// The store owner, not this producer, adds and charges candidate allowances.
type PublicStateCountSink interface {
	Entry() error
	Binding() error
	Host() error
	Slot() error
}

// PublicStateProjectionSchema holds only finite names derived from the sole
// API DTO descriptors. Construct it once INSIDE the complete inspection, so
// startup accounting includes it. It retains no source, state or permission.
type PublicStateProjectionSchema struct {
	state, intent, binding, retired, primary, minted, shape, slot []string
	trusted                                                       []string
}

// NewPublicStateProjectionSchema derives the same names as ExactJSONFields.
// This is DTO metadata only, never a source/receipt/inspection constructor.
func NewPublicStateProjectionSchema() PublicStateProjectionSchema {
	return PublicStateProjectionSchema{
		state:   api.ExactJSONFieldNames((*api.SessionSecretState)(nil)),
		intent:  api.ExactJSONFieldNames((*api.SecretDeliveryIntent)(nil)),
		trusted: api.ExactJSONFieldNames((*api.TrustedSecretUseV1)(nil)),
		binding: api.ExactJSONFieldNames((*api.SecretBind)(nil)),
		retired: api.ExactJSONFieldNames((*api.RetiredPrimarySecretState)(nil)),
		primary: api.ExactJSONFieldNames((*api.PublicSecretState)(nil)),
		minted:  api.ExactJSONFieldNames((*api.PublicMintedSecretState)(nil)),
		shape:   api.ExactJSONFieldNames((*api.SecretShape)(nil)),
		slot:    api.ExactJSONFieldNames((*api.SecretShapeSlot)(nil)),
	}
}

type projectedStateCountSpans struct {
	version, intent, primary, minted, retired api.JSONSpan
}

func projectedStateCountFields(p *api.JSONProjection, span api.JSONSpan, fields []string) (projectedStateCountSpans, error) {
	var values projectedStateCountSpans
	err := p.Object(span, fields, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		switch fields[field.Known] {
		case "version":
			values.version = field.Value
		case "intent":
			values.intent = field.Value
		case "primary":
			values.primary = field.Value
		case "minted":
			values.minted = field.Value
		case "retired":
			values.retired = field.Value
		case "generation":
			// This pass counts source; the canonical semantic pass validates it.
		default:
			return errProjectedStateOpaque
		}
		return nil
	})
	return values, projectedOpaqueError(err)
}

func projectedIntentCountFields(p *api.JSONProjection, span api.JSONSpan, fields []string) (version, modes, binds, trusted api.JSONSpan, err error) {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return version, modes, binds, trusted, err
	}
	err = p.Object(span, fields, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		switch fields[field.Known] {
		case "version":
			version = field.Value
		case "binds":
			binds = field.Value
		case "modes":
			modes = field.Value
		case "trusted_uses":
			trusted = field.Value
		default:
			return errProjectedStateOpaque
		}
		return nil
	})
	return version, modes, binds, trusted, projectedOpaqueError(err)
}

// This raw pass counts physical occurrences. The admitted grouped index
// decides exact duplicate equality before Validate can authorise a known DTO.
func projectedStoredMap(p *api.JSONProjection, span api.JSONSpan, visit func(api.JSONField) error) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	fields, err := p.Fields(span)
	if err != nil {
		return projectedOpaqueError(err)
	}
	for {
		field, exists, err := fields.Next()
		if err != nil {
			return projectedOpaqueError(err)
		}
		if !exists {
			return nil
		}
		if err := visit(field); err != nil {
			return err
		}
	}

}

func projectedStoredArray(p *api.JSONProjection, span api.JSONSpan, visit func(api.JSONSpan) error) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	values, err := p.Values(span)
	if err != nil {
		return projectedOpaqueError(err)
	}
	for {
		value, exists, err := values.Next()
		if err != nil {
			return projectedOpaqueError(err)
		}
		if !exists {
			return nil
		}
		if err := visit(value); err != nil {
			return err
		}
	}
}

func (s PublicStateProjectionSchema) countBinding(p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	var hosts api.JSONSpan
	err = p.Object(span, s.binding, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		switch s.binding[field.Known] {
		case "hosts":
			hosts = field.Value
		case "header", "format":
			// Canonical field/format validation is not replaced by this count.
		default:
			return errProjectedStateOpaque
		}
		return nil
	})
	if err != nil {
		return projectedOpaqueError(err)
	}
	return projectedStoredArray(p, hosts, func(host api.JSONSpan) error {
		if err := counter.Host(); err != nil {
			return err
		}
		// Validate typed strings/null without keeping a host or its padding.
		return walkProjectedString(p, host, func(rune) error { return nil })
	})
}

func (s PublicStateProjectionSchema) countRetired(p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	if err := counter.Entry(); err != nil {
		return err
	}
	if err := counter.Binding(); err != nil {
		return err
	}
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	var binding, shape api.JSONSpan
	err = p.Object(span, s.retired, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		switch s.retired[field.Known] {
		case "binding":
			binding = field.Value
		case "shape":
			shape = field.Value
		case "sentinel", "identity":
			// These still require the complete canonical per-entry validation.
		default:
			return errProjectedStateOpaque
		}
		return nil
	})
	if err != nil {
		return projectedOpaqueError(err)
	}
	if err := s.countShape(p, shape, counter); err != nil {
		return err
	}
	return s.countBinding(p, binding, counter)
}

// Count visits one known stored state's actual records in the SAME retained
// JSON source. Input map order does not change support or semantics.
// Numeric descriptors supply canonical lookup without materialising keys. Complete root validation and
// unchanged source epoch are preconditions. Success counts quota inputs only;
// every canonical semantic/domain/alias check is still mandatory afterwards.
func (s PublicStateProjectionSchema) Count(ctx context.Context, p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	if err := sentinelContext(ctx, "receipt"); err != nil {
		return err
	}
	if p == nil || counter == nil || len(s.state) == 0 || len(s.intent) == 0 || len(s.binding) == 0 || len(s.retired) == 0 {
		return invalidPublicSessionState()
	}
	fields, err := projectedStateCountFields(p, span, s.state)
	if err != nil {
		return err
	}
	version, err := projectedNativeInt(p, fields.version)
	if err != nil {
		return err
	}
	if version != api.SessionSecretStateV1 {
		return errProjectedStateOpaque
	}
	intentVersion, modes, binds, trusted, err := projectedIntentCountFields(p, fields.intent, s.intent)
	if err != nil {
		return err
	}
	intent, err := projectedNativeInt(p, intentVersion)
	if err != nil {
		return err
	}
	if intent != api.SecretDeliveryIntentV1 {
		return errProjectedStateOpaque
	}
	for _, mapSpan := range []api.JSONSpan{modes, binds, fields.primary, fields.minted, fields.retired, trusted} {
		if err := s.countMapIndex(p, mapSpan, counter); err != nil {
			return err
		}
	}
	if err := projectedStoredMap(p, fields.primary, func(field api.JSONField) error { return s.countPublicEntry(p, field.Value, s.primary, counter) }); err != nil {
		return err
	}
	if err := projectedStoredMap(p, binds, func(field api.JSONField) error {
		if err := counter.Binding(); err != nil {
			return err
		}
		return s.countBinding(p, field.Value, counter)
	}); err != nil {
		return err
	}
	if err := projectedStoredMap(p, fields.minted, func(field api.JSONField) error {
		return projectedStoredArray(p, field.Value, func(value api.JSONSpan) error { return s.countPublicEntry(p, value, s.minted, counter) })
	}); err != nil {
		return err
	}
	return projectedStoredMap(p, fields.retired, func(field api.JSONField) error {
		return projectedStoredArray(p, field.Value, func(value api.JSONSpan) error { return s.countRetired(p, value, counter) })
	})
}

// CountProjectedSessionSecretState is the one-state convenience adapter.
// A multi-state inspection constructs its schema once and calls schema.Count
// inside its actual StateCounter callback. No declared/wire count is accepted.
func CountProjectedSessionSecretState(ctx context.Context, p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	return NewPublicStateProjectionSchema().Count(ctx, p, span, counter)
}

// Shape slots are physical state records too. Counting just credential rows
// undercharges states with many compact runs, even though each entry may
// remain below the unchanged control-format limit.
func (s PublicStateProjectionSchema) countShape(p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	var slots api.JSONSpan
	err = p.Object(span, s.shape, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		if s.shape[field.Known] == "slots" {
			slots = field.Value
		}
		return nil
	})
	if err != nil {
		return projectedOpaqueError(err)
	}
	return projectedStoredArray(p, slots, func(api.JSONSpan) error { return counter.Slot() })
}

func (s PublicStateProjectionSchema) countPublicEntry(p *api.JSONProjection, span api.JSONSpan, fields []string, counter PublicStateCountSink) error {
	if err := counter.Entry(); err != nil {
		return err
	}
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	var shape api.JSONSpan
	err = p.Object(span, fields, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		if fields[field.Known] == "shape" {
			shape = field.Value
		}
		if fields[field.Known] == "authority" {
			return counter.Host()
		}
		return nil
	})
	if err != nil {
		return projectedOpaqueError(err)
	}
	return s.countShape(p, shape, counter)
}

// Each present source map owns one binding-layout descriptor and one
// host-layout name/value descriptor per visited key. These are physical
// records in the SAME grouped tree, not a second map or declared quota.
func (s PublicStateProjectionSchema) countMapIndex(p *api.JSONProjection, span api.JSONSpan, counter PublicStateCountSink) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	if err := counter.Binding(); err != nil {
		return err
	}
	return projectedStoredMap(p, span, func(api.JSONField) error { return counter.Host() })
}
