package egress

import (
	"context"
	"errors"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
)

var errProjectedStateOpaque = errors.New("projected public state is structurally unsupported")

// savedShapeJSONSchema is finite DTO metadata for one complete inspection.
// Input fields, names and values are not retained in this descriptor.
type savedShapeJSONSchema struct{ shape, slot []string }

func newSavedShapeJSONSchema() savedShapeJSONSchema {
	return savedShapeJSONSchema{
		shape: api.ExactJSONFieldNames((*api.SecretShape)(nil)),
		slot:  api.ExactJSONFieldNames((*api.SecretShapeSlot)(nil)),
	}
}

func projectedOpaqueError(err error) error {
	if errors.Is(err, api.ErrJSONProjection) {
		return errors.Join(errProjectedStateOpaque, err)
	}
	return err
}

func projectedNull(p interface {
	Literal(api.JSONSpan, string) (bool, error)
}, span api.JSONSpan) (bool, error) {
	if span == (api.JSONSpan{}) {
		return true, nil
	}
	return p.Literal(span, "null")
}

// walkProjectedString preserves encoding/json's null-as-zero string rule.
// The existing projection owns all JSON/escape grammar and source reads.
func walkProjectedString(p TrustedUseProjectionV1, span api.JSONSpan, visit func(rune) error) error {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return err
	}
	return projectedOpaqueError(p.WalkString(span, visit))
}

// projectedNativeInt checks only the DTO integer conversion over an already
// validated JSON value span. JSON syntax belongs to the ONE projection.
func projectedNativeInt(p TrustedUseProjectionV1, span api.JSONSpan) (int, error) {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return 0, err
	}
	var magnitude uint64
	negative, first, digit := false, true, false
	limit := uint64(1)<<(strconv.IntSize-1) - 1
	err = p.WalkEncoded(span, func(part []byte) error {
		for _, b := range part {
			if first {
				first = false
				if b == '-' {
					negative = true
					limit++
					continue
				}
			}
			if b < '0' || b > '9' || magnitude > (limit-uint64(b-'0'))/10 {
				return errProjectedStateOpaque
			}
			digit = true
			magnitude = magnitude*10 + uint64(b-'0')
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if !digit {
		return 0, errProjectedStateOpaque
	}
	if negative && magnitude == uint64(1)<<(strconv.IntSize-1) {
		return -int(magnitude-1) - 1, nil //nolint:gosec // G115: magnitude-1 is the largest native positive integer
	}
	value := int(magnitude) //nolint:gosec // G115: bounded above by the native positive limit
	if negative {
		value = -value
	}
	return value, nil
}

func projectedShapeSlot(ctx context.Context, p *api.JSONProjection, span api.JSONSpan, fields []string) (symbolicSavedClass, int, error) {
	if err := sentinelContext(ctx, "receipt"); err != nil {
		return symbolicSavedClass{}, 0, err
	}
	var classSpan, asciiSpan, countSpan, widthSpan api.JSONSpan
	null, err := projectedNull(p, span)
	if err != nil {
		return symbolicSavedClass{}, 0, err
	}
	if !null {
		err = p.Object(span, fields, func(field api.JSONField) error {
			if field.Known < 0 {
				return errProjectedStateOpaque
			}
			switch fields[field.Known] {
			case "class":
				classSpan = field.Value
			case "ascii":
				asciiSpan = field.Value
			case "count":
				countSpan = field.Value
			case "width":
				widthSpan = field.Value
			default:
				return errProjectedStateOpaque
			}
			return nil
		})
	}
	if err != nil {
		return symbolicSavedClass{}, 0, projectedOpaqueError(err)
	}
	count, err := projectedNativeInt(p, countSpan)
	if err != nil {
		return symbolicSavedClass{}, 0, err
	}
	width, err := projectedNativeInt(p, widthSpan)
	if err != nil {
		return symbolicSavedClass{}, 0, err
	}
	var label savedClassLabel
	label.reset()
	err = walkProjectedString(p, classSpan, func(r rune) error {
		if !label.rune(r) {
			return sentinelFailure("receipt", "saved token class is unsupported")
		}
		return nil
	})
	if err != nil {
		return symbolicSavedClass{}, 0, err
	}
	class, ok := newSymbolicSavedClass(label.finish(), width)
	if !ok {
		return symbolicSavedClass{}, 0, sentinelFailure("receipt", "saved token class is unsupported")
	}
	err = walkProjectedString(p, asciiSpan, func(r rune) error {
		if !class.asciiRune(r) {
			return sentinelFailure("receipt", "saved ASCII token class is invalid")
		}
		return nil
	})
	if err != nil {
		return symbolicSavedClass{}, 0, err
	}
	if err := class.finish(ctx, "receipt"); err != nil {
		return symbolicSavedClass{}, 0, err
	}
	return class, count, nil
}

func projectedSavedRule(p *api.JSONProjection, span api.JSONSpan) (string, error) {
	null, err := projectedNull(p, span)
	if err != nil || null {
		return "", err
	}
	empty, err := p.StringEqual(span, "")
	if err != nil || empty {
		return "", projectedOpaqueError(err)
	}
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		return "", err
	}
	for _, rule := range rules.rules {
		equal, err := p.StringEqual(span, rule.id)
		if err != nil {
			return "", projectedOpaqueError(err)
		}
		if equal {
			return rule.id, nil
		}
	}
	return "", memberError("bad_secret", "saved token grammar is unsupported", "Start a fresh sandbox with a supported credential format.")
}

// projectedShapeMetrics is a partial component result, never Supported or
// authority. Nonempty rule still requires the SAME full pinned-path walk;
// value/prefix matching, bindings and every global relation remain mandatory.
type projectedShapeMetrics struct {
	metrics symbolicShapeMetrics
	rule    string
	slots   api.JSONSpan
}

// inspectProjectedShapeMetrics uses the delivered ONE JSON cursor. The
// caller must first validate the complete root and keep the SAME owned source
// epoch unchanged. expectedPrefix is already matched to canonical local data;
// this helper never selects a name, prefix, grant, receipt or fetch capability.
func inspectProjectedShapeMetrics(ctx context.Context, p *api.JSONProjection, span api.JSONSpan, schema savedShapeJSONSchema, expectedPrefix string, policy sentinelPunctuationPolicy) (projectedShapeMetrics, error) {
	if err := sentinelContext(ctx, "receipt"); err != nil {
		return projectedShapeMetrics{}, err
	}
	if p == nil {
		return projectedShapeMetrics{}, invalidPublicSessionState()
	}
	null, err := projectedNull(p, span)
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	if null {
		return projectedShapeMetrics{}, sentinelFailure("receipt", "saved public token metadata is invalid")
	}
	var versionSpan, prefixSpan, ruleSpan, slotsSpan api.JSONSpan
	err = p.Object(span, schema.shape, func(field api.JSONField) error {
		if field.Known < 0 {
			return errProjectedStateOpaque
		}
		switch schema.shape[field.Known] {
		case "version":
			versionSpan = field.Value
		case "prefix":
			prefixSpan = field.Value
		case "rule":
			ruleSpan = field.Value
		case "slots":
			slotsSpan = field.Value
		default:
			return errProjectedStateOpaque
		}
		return nil
	})
	if err != nil {
		return projectedShapeMetrics{}, projectedOpaqueError(err)
	}
	version, err := projectedNativeInt(p, versionSpan)
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	if version != api.SecretShapeV1 {
		return projectedShapeMetrics{}, sentinelFailure("receipt", "saved public token metadata is invalid")
	}
	rule, err := projectedSavedRule(p, ruleSpan)
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	nullPrefix, err := projectedNull(p, prefixSpan)
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	prefixEqual := expectedPrefix == "" && nullPrefix
	if !nullPrefix {
		prefixEqual, err = p.StringEqual(prefixSpan, expectedPrefix)
		if err != nil {
			return projectedShapeMetrics{}, projectedOpaqueError(err)
		}
	}
	if !prefixEqual {
		return projectedShapeMetrics{}, sentinelFailure("receipt", "saved public prefix is not canonical")
	}
	var state symbolicShapeValidator
	state.reset(ctx, "receipt")
	if err := walkProjectedString(p, prefixSpan, state.prefixRune); err != nil {
		return projectedShapeMetrics{}, err
	}
	nullSlots, err := projectedNull(p, slotsSpan)
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	if !nullSlots {
		values, err := p.Values(slotsSpan)
		if err != nil {
			return projectedShapeMetrics{}, projectedOpaqueError(err)
		}
		anySlot := false
		for {
			slotSpan, exists, err := values.Next()
			if err != nil {
				return projectedShapeMetrics{}, projectedOpaqueError(err)
			}
			if !exists {
				break
			}
			anySlot = true
			class, count, err := projectedShapeSlot(ctx, p, slotSpan, schema.slot)
			if err != nil {
				return projectedShapeMetrics{}, err
			}
			if rule == "" && !class.generic(policy) {
				return projectedShapeMetrics{}, sentinelFailure("receipt", "saved generic class is not a trusted source class")
			}
			if count <= 0 {
				return projectedShapeMetrics{}, sentinelFailure("receipt", "saved token run is not canonical")
			}
			if err := state.slot(class, uint64(count)); err != nil {
				return projectedShapeMetrics{}, err
			}
		}
		if !anySlot {
			return projectedShapeMetrics{}, sentinelFailure("receipt", "saved token class encoding is not canonical")
		}
	}
	metrics, err := state.finish()
	if err != nil {
		return projectedShapeMetrics{}, err
	}
	return projectedShapeMetrics{metrics: metrics, rule: rule, slots: slotsSpan}, nil
}
