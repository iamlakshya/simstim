package egress

import (
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type fdShape struct {
	prefix, rule, slots api.JSONSpan
	metrics             projectedShapeMetrics
	prefixText          string // trusted finite agent data, never observed source text
	base, count         uint64
}

func (s *fdPublicState) shape(name, span api.JSONSpan, binding uint64, minted bool) (fdShape, error) {
	var shape fdShape
	err := fdFields(s.p, span, s.shapes.shape, func(key string, value api.JSONSpan) error {
		switch key {
		case "prefix":
			shape.prefix = value
		case "rule":
			shape.rule = value
		case "slots":
			shape.slots = value
		}
		return nil
	})
	if err != nil {
		return shape, err
	}
	if minted {
		empty, err := fdStringEqual(s.p, shape.prefix, "")
		if err != nil {
			return shape, err
		}
		if !empty {
			return shape, invalidPublicSessionState()
		}
	} else {
		shape.prefixText, err = fdSavedPrefix(s.p, name, shape.prefix)
		if err != nil {
			return shape, err
		}
	}
	bind, err := s.region.Binding(binding)
	if err != nil {
		return shape, err
	}
	policy := sentinelPunctuationPrintable
	if bind.FormatKind == 2 {
		policy = sentinelPunctuationToken68
	}
	shape.metrics, err = inspectProjectedShapeMetrics(s.ctx, s.p, span, s.shapes, shape.prefixText, policy)
	if err != nil {
		return shape, err
	}
	if !minted && !shape.metrics.metrics.variable {
		return shape, invalidPublicSessionState()
	}
	err = projectedStoredArray(s.p, shape.slots, func(value api.JSONSpan) error {
		class, count, err := projectedShapeSlot(s.ctx, s.p, value, s.shapes.slot)
		if err != nil {
			return err
		}
		var ascii api.JSONSpan
		err = fdFields(s.p, value, s.shapes.slot, func(key string, value api.JSONSpan) error {
			if key == "ascii" {
				ascii = value
			}
			return nil
		})
		if err != nil {
			return err
		}
		id, err := s.region.AppendSlot(tomlinspect.SlotRecord{Kind: uint8(class.kind), Width: uint8(class.width), Count: uint64(count), ASCIISpan: fdSpan(ascii)})
		if err != nil {
			return err
		}
		if shape.count == 0 {
			shape.base = id
		}
		shape.count++
		return nil
	})
	return shape, err
}

func (s *fdPublicState) class(slot tomlinspect.SlotRecord) (symbolicSavedClass, error) {
	class, ok := newSymbolicSavedClass(savedClassKind(slot.Kind), int(slot.Width))
	if !ok {
		return class, invalidPublicSessionState()
	}
	err := walkProjectedString(s.p, jsonSpan(slot.ASCIISpan), func(r rune) error {
		if !class.asciiRune(r) {
			return invalidPublicSessionState()
		}
		return nil
	})
	if err != nil {
		return class, err
	}
	if err := class.finish(s.ctx, "receipt"); err != nil {
		return class, err
	}
	return class, nil
}

func (s *fdPublicState) sentinel(span api.JSONSpan, shape fdShape, binding uint64) error {
	value, err := s.p.StringRunes(span)
	if err != nil {
		return err
	}
	bind, err := s.region.Binding(binding)
	if err != nil {
		return err
	}
	var field CredentialFieldValidator
	field.Reset(s.ctx, fdFormatName(bind.FormatKind), bind.UserBytes)
	var scratch [utf8.UTFMax]byte
	consume := func() (rune, error) {
		r, ok, err := value.Next()
		if err != nil {
			return 0, err
		}
		if !ok || !sentinelRune(r) {
			return 0, invalidPublicSessionState()
		}
		if _, err := field.Write(utf8.AppendRune(scratch[:0], r)); err != nil {
			return 0, err
		}
		return r, nil
	}
	for _, expected := range shape.prefixText {
		r, err := consume()
		if err != nil {
			return err
		}
		if r != expected {
			return invalidPublicSessionState()
		}
	}
	for index := uint64(0); index < shape.count; index++ {
		slot, err := s.region.Slot(shape.base + index)
		if err != nil {
			return err
		}
		class, err := s.class(slot)
		if err != nil {
			return err
		}
		for range slot.Count {
			r, err := consume()
			if err != nil {
				return err
			}
			if !class.contains(r) {
				return invalidPublicSessionState()
			}
		}
	}
	if _, ok, err := value.Next(); err != nil {
		return err
	} else if ok {
		return invalidPublicSessionState()
	}
	length, err := field.Finish()
	if err != nil {
		return err
	}
	if length > maxHTTPHeaderBytes || bind.HeaderBytes > maxHTTPHeaderBytes-length || bind.HeaderBytes+length > maxHTTPHeaderBytes-4 {
		return invalidPublicSessionState()
	}
	if shape.metrics.metrics.bytes == 0 {
		return invalidPublicSessionState()
	}
	return nil
}

func (s *fdPublicState) sameShape(a, b tomlinspect.SemanticEntry) (bool, error) {
	if a.SlotCount != b.SlotCount {
		return false, nil
	}
	for _, pair := range [][2]tomlinspect.Span{{a.Prefix, b.Prefix}, {a.Rule, b.Rule}} {
		equal, err := fdSameOptionalString(s.p, jsonSpan(pair[0]), jsonSpan(pair[1]))
		if err != nil || !equal {
			return false, err
		}
	}
	for index := uint64(0); index < a.SlotCount; index++ {
		x, err := s.region.Slot(a.SlotBase + index)
		if err != nil {
			return false, err
		}
		y, err := s.region.Slot(b.SlotBase + index)
		if err != nil {
			return false, err
		}
		if x.Kind != y.Kind || x.Width != y.Width || x.Count != y.Count {
			return false, nil
		}
		equal, err := fdSameOptionalString(s.p, jsonSpan(x.ASCIISpan), jsonSpan(y.ASCIISpan))
		if err != nil || !equal {
			return false, err
		}
	}
	return true, nil
}
