package egress

import (
	"context"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type symbolicShapeMetrics struct {
	bytes, prefixBytes int
	variable           bool
}

// symbolicShapeValidator checks one shape's ordered source runs without
// retaining a slot list or an expanded alphabet. It grants no authority.
type symbolicShapeValidator struct {
	ctx      context.Context
	name     string
	metrics  symbolicShapeMetrics
	previous symbolicSavedClass
	hasSlot  bool
	finished bool
	err      error
}

func (s *symbolicShapeValidator) reset(ctx context.Context, name string) {
	*s = symbolicShapeValidator{ctx: ctx, name: name}
}

func (s *symbolicShapeValidator) check() error {
	if err := sentinelContext(s.ctx, s.name); err != nil {
		s.err = err
	}
	return s.err
}

func (s *symbolicShapeValidator) fail(reason string) error {
	s.err = sentinelFailure(s.name, reason)
	return s.err
}

func (s *symbolicShapeValidator) prefixRune(r rune) error {
	if err := s.check(); err != nil {
		return err
	}
	width := utf8.RuneLen(r)
	if s.finished || s.hasSlot || width < 1 || !sentinelRune(r) || s.metrics.bytes > proto.MaxFrame-width {
		return s.fail("unsupported known-prefix representation")
	}
	s.metrics.bytes += width
	s.metrics.prefixBytes += width
	return nil
}

func sameSymbolicSavedClass(a, b symbolicSavedClass) bool {
	if !a.valid || a.invalid || !b.valid || b.invalid || a.kind != b.kind || a.width != b.width || a.used != b.used {
		return false
	}
	for i := range a.used {
		if a.ascii[i] != b.ascii[i] {
			return false
		}
	}
	return true
}

func (s *symbolicShapeValidator) slot(class symbolicSavedClass, count uint64) error {
	if err := s.check(); err != nil {
		return err
	}
	if s.finished || !class.valid || class.invalid || count == 0 || s.hasSlot && sameSymbolicSavedClass(s.previous, class) {
		return s.fail("saved token run is not canonical")
	}
	if class.width < 1 || count > uint64((proto.MaxFrame-s.metrics.bytes)/class.width) {
		return s.fail("token shape exceeds the control representation limit")
	}
	s.metrics.bytes += int(count) * class.width //nolint:gosec // G115: the existing control bound above is at most proto.MaxFrame
	s.metrics.variable = s.metrics.variable || class.variable
	s.previous, s.hasSlot = class, true
	return nil
}

func (s *symbolicShapeValidator) finish() (symbolicShapeMetrics, error) {
	if err := s.check(); err != nil {
		return symbolicShapeMetrics{}, err
	}
	s.finished = true
	return s.metrics, nil
}

func (symbolicShapeValidator) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (symbolicShapeMetrics) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }

// symbolicPublicShapeMetrics is a selected typed component adapter. It is
// not saved-state validation: rule lookup/path, stand-in matching, binding,
// aliases and local provenance remain separate required checks. The full
// production decoder/compiler remains the independent differential oracle.
func symbolicPublicShapeMetrics(ctx context.Context, name string, public *api.SecretShape, policy sentinelPunctuationPolicy) (symbolicShapeMetrics, error) {
	if public == nil || public.Version != api.SecretShapeV1 || !utf8.ValidString(public.Prefix) || public.Slots != nil && len(public.Slots) == 0 {
		return symbolicShapeMetrics{}, sentinelFailure(name, "saved token class encoding is not canonical")
	}
	var state symbolicShapeValidator
	state.reset(ctx, name)
	for _, r := range public.Prefix {
		if err := state.prefixRune(r); err != nil {
			return symbolicShapeMetrics{}, err
		}
	}
	for _, slot := range public.Slots {
		class, err := symbolicPublicClass(ctx, name, slot)
		if err != nil {
			return symbolicShapeMetrics{}, err
		}
		if public.Rule == "" && !class.generic(policy) {
			return symbolicShapeMetrics{}, sentinelFailure(name, "saved generic class is not a trusted source class")
		}
		if slot.Count <= 0 {
			return symbolicShapeMetrics{}, sentinelFailure(name, "saved token run is not canonical")
		}
		if err := state.slot(class, uint64(slot.Count)); err != nil {
			return symbolicShapeMetrics{}, err
		}
	}
	return state.finish()
}
