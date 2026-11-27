package egress

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

type savedClassKind uint8

const (
	savedClassInvalid savedClassKind = iota
	savedClassUpper
	savedClassLower
	savedClassDigit
	savedClassPrintable
	savedClassToken68
	savedClassASCII
	savedClassUnicodeUpper
	savedClassUnicodeLower
	savedClassUnicodeDigit
)

var savedClassLabels = [...]string{
	"ascii-upper", "ascii-lower", "ascii-digit", "ascii-printable-punct", "ascii-token68-punct", "ascii",
	"unicode-upper", "unicode-lower", "unicode-digit",
}

// savedClassLabel checks the finite labels without retaining an input string.
type savedClassLabel struct {
	possible uint16
	position int
}

func (savedClassLabel) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (s *savedClassLabel) reset() { *s = savedClassLabel{possible: 1<<len(savedClassLabels) - 1} }

func (s *savedClassLabel) rune(r rune) bool {
	for i, label := range savedClassLabels {
		if s.possible&(1<<i) != 0 && (s.position >= len(label) || rune(label[s.position]) != r) {
			s.possible &^= 1 << i
		}
	}
	if s.possible == 0 {
		return false
	}
	s.position++
	return true
}

func (s savedClassLabel) finish() savedClassKind {
	for i, label := range savedClassLabels {
		if s.possible&(1<<i) != 0 && s.position == len(label) {
			return savedClassKind(i + 1) //nolint:gosec // G115: the finite label table has nine entries
		}
	}
	return savedClassInvalid
}

// symbolicSavedClass retains only a single canonical class. ASCII choices
// are unique and below128, so the fixed order buffer is not a new input cap.
// Unicode membership uses the same pinned standard Unicode tables.
type symbolicSavedClass struct {
	kind     savedClassKind
	width    int
	ascii    [128]byte
	seen     [2]uint64
	used     int
	variable bool
	valid    bool
	invalid  bool
}

func (symbolicSavedClass) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newSymbolicSavedClass(kind savedClassKind, width int) (symbolicSavedClass, bool) {
	value := symbolicSavedClass{kind: kind, width: width}
	if kind >= savedClassUpper && kind <= savedClassASCII {
		value.invalid = width != 1
		return value, !value.invalid
	}
	value.invalid = !(kind >= savedClassUnicodeUpper && kind <= savedClassUnicodeDigit && width >= 2 && width <= utf8.UTFMax)
	return value, !value.invalid
}

func (s *symbolicSavedClass) asciiRune(r rune) bool {
	if s.invalid || s.valid || s.kind != savedClassASCII || r < 0 || r >= utf8.RuneSelf || !sentinelRune(r) {
		s.valid, s.invalid = false, true
		return false
	}
	bit := uint64(1) << (uint(r) & 63)
	word := uint(r) / 64
	if s.seen[word]&bit != 0 || s.used >= len(s.ascii) {
		s.invalid = true
		return false
	}
	s.seen[word] |= bit
	s.ascii[s.used] = byte(r) //nolint:gosec // G115: ASCII range checked above
	s.used++
	return true
}

func namedSavedClassAlphabet(kind savedClassKind) string {
	switch kind {
	case savedClassUpper:
		return sentinelUpper
	case savedClassLower:
		return sentinelLower
	case savedClassDigit:
		return sentinelDigits
	case savedClassPrintable:
		return sentinelPrintablePunctuation
	case savedClassToken68:
		return sentinelToken68Punctuation
	}
	return ""
}

func (s symbolicSavedClass) equalASCII(want string) bool {
	if s.used != len(want) {
		return false
	}
	for i := range s.used {
		if s.ascii[i] != want[i] {
			return false
		}
	}
	return true
}

func savedClassUnicodeTable(kind savedClassKind) *unicode.RangeTable {
	switch kind {
	case savedClassUnicodeUpper:
		return unicode.Lu
	case savedClassUnicodeLower:
		return unicode.Ll
	case savedClassUnicodeDigit:
		return unicode.Nd
	}
	return nil
}

// finish validates the same public-class round trip and duplicate-free
// compilation as the full path, without producing a Unicode alphabet.
func (s *symbolicSavedClass) finish(ctx context.Context, name string) error {
	if err := sentinelContext(ctx, name); err != nil {
		s.valid, s.invalid = false, true
		return err
	}
	if s.invalid {
		return sentinelFailure(name, "saved token class is unsupported")
	}
	if s.valid {
		return nil
	}
	if alphabet := namedSavedClassAlphabet(s.kind); alphabet != "" {
		s.variable, s.valid = len(alphabet) > 1, true
		return nil
	}
	if s.kind == savedClassASCII {
		if s.used == 0 {
			s.invalid = true
			return sentinelFailure(name, "saved ASCII token class is invalid")
		}
		for kind := savedClassUpper; kind <= savedClassToken68; kind++ {
			if s.equalASCII(namedSavedClassAlphabet(kind)) {
				s.invalid = true
				return sentinelFailure(name, "saved token class encoding is not canonical")
			}
		}
		s.variable, s.valid = s.used > 1, true
		return nil
	}
	table := savedClassUnicodeTable(s.kind)
	if table == nil || s.width < 2 || s.width > utf8.UTFMax {
		s.invalid = true
		return sentinelFailure(name, "saved token class is unsupported")
	}
	low, high := uint32(0x80), uint32(0x7ff)
	if s.width == 3 {
		low, high = 0x800, 0xffff
	} else if s.width == 4 {
		low, high = 0x10000, utf8.MaxRune
	}
	choices := uint32(0)
	add := func(start, end, stride uint32) {
		if end < low || start > high || choices >= 2 {
			return
		}
		if start < low {
			start += (low - start + stride - 1) / stride * stride
		}
		end = min(end, high)
		if start <= end {
			choices = min(uint32(2), choices+1+(end-start)/stride)
		}
	}
	// Lu/Ll/Nd do not contain controls, format characters or NUL. These
	// range intersections therefore count exactly the same width-filtered
	// alphabet as sentinelUnicodeAlphabet, stopping once variability is known.
	for _, span := range table.R16 {
		if err := sentinelContext(ctx, name); err != nil {
			s.invalid = true
			return err
		}
		add(uint32(span.Lo), uint32(span.Hi), uint32(span.Stride))
		if choices >= 2 {
			break
		}
	}
	for _, span := range table.R32 {
		if choices >= 2 {
			break
		}
		if err := sentinelContext(ctx, name); err != nil {
			s.invalid = true
			return err
		}
		add(span.Lo, span.Hi, span.Stride)
	}
	if choices == 0 {
		s.invalid = true
		return sentinelFailure(name, "Unicode token class has no same-width alphabet")
	}
	s.variable, s.valid = choices > 1, true
	return nil
}

func (s symbolicSavedClass) contains(r rune) bool {
	if !s.valid || s.invalid || utf8.RuneLen(r) != s.width || !sentinelRune(r) {
		return false
	}
	if alphabet := namedSavedClassAlphabet(s.kind); alphabet != "" {
		return strings.ContainsRune(alphabet, r)
	}
	if s.kind == savedClassASCII {
		return r >= 0 && r < utf8.RuneSelf && s.seen[uint(r)/64]&(uint64(1)<<(uint(r)&63)) != 0
	}
	table := savedClassUnicodeTable(s.kind)
	return table != nil && unicode.Is(table, r)
}

func (s symbolicSavedClass) generic(policy sentinelPunctuationPolicy) bool {
	if !s.valid || s.invalid {
		return false
	}
	switch s.kind {
	case savedClassUpper, savedClassLower, savedClassDigit, savedClassUnicodeUpper, savedClassUnicodeLower, savedClassUnicodeDigit:
		return true
	case savedClassPrintable:
		return policy == sentinelPunctuationPrintable
	case savedClassToken68:
		return policy == sentinelPunctuationToken68
	case savedClassASCII:
		return s.used == 1 && strings.ContainsRune(sentinelSeparators, rune(s.ascii[0]))
	}
	return false
}

// symbolicPublicClass is the selected typed adapter for differential checks.
// The production full decoder remains unchanged until that evidence exists.
func symbolicPublicClass(ctx context.Context, name string, slot api.SecretShapeSlot) (symbolicSavedClass, error) {
	var label savedClassLabel
	label.reset()
	for _, r := range slot.Class {
		if !label.rune(r) {
			return symbolicSavedClass{}, sentinelFailure(name, "saved token class is unsupported")
		}
	}
	value, ok := newSymbolicSavedClass(label.finish(), slot.Width)
	if !ok {
		return symbolicSavedClass{}, sentinelFailure(name, "saved token class is unsupported")
	}
	for i, r := range slot.ASCII {
		if i%128 == 0 {
			if err := sentinelContext(ctx, name); err != nil {
				return symbolicSavedClass{}, err
			}
		}
		if !value.asciiRune(r) {
			return symbolicSavedClass{}, sentinelFailure(name, "saved ASCII token class is invalid")
		}
	}
	if err := value.finish(ctx, name); err != nil {
		return symbolicSavedClass{}, err
	}
	return value, nil
}
