package tomlinspect

import (
	"errors"
	"math"
)

// ErrNumberRange has the pin's native int64/float64 range classification.
// The rejected input is deliberately absent from the retained error.
var ErrNumberRange = errors.New("TOML native number is out of range")

// numberSpan is the string-indexing view used by the pinned Go converters.
// Every indexed read checks cancellation through source.at, including hits.
// Underscores remain physical source bytes. Both readFloat and decimal.set
// skip them after the TOML preflight, as the pin does before ParseFloat.
type numberSpan struct {
	source *source
	span   Span
}

func (s numberSpan) len() int { return int(s.span.End - s.span.Start) }
func (s numberSpan) at(i int) byte {
	if i < 0 || i >= s.len() {
		panic("pinned native numeric cursor invariant exceeded")
	}
	b, err := s.source.at(s.span.Start + int64(i))
	if err != nil {
		panic(err)
	}
	return b
}
func (s numberSpan) after(i int) numberSpan {
	s.span.Start += int64(i)
	return s
}
func (s numberSpan) prefix(i int) numberSpan {
	s.span.End = s.span.Start + int64(i)
	return s
}
func (s numberSpan) equal(want string) bool {
	if s.len() != len(want) {
		return false
	}
	for i := range len(want) {
		if s.at(i) != want[i] {
			return false
		}
	}
	return true
}

// These three checks are the pinned parse.go TOML checks, ahead of the
// native Go conversion. In particular, Go's looser base-prefix underscore
// syntax is not enough to accept an integer, nor is Go's "1." a TOML float.
func numHasLeadingZero(s numberSpan) bool {
	if s.len() > 1 && s.at(0) == '0' && !(s.at(1) == 'b' || s.at(1) == 'o' || s.at(1) == 'x') {
		return true
	}
	return s.len() > 2 && (s.at(0) == '-' || s.at(0) == '+') && s.at(1) == '0'
}

func numUnderscoresOK(s numberSpan) bool {
	for _, special := range [...]string{"nan", "+nan", "-nan", "inf", "-inf", "+inf"} {
		if s.equal(special) {
			return true
		}
	}
	accept := false
	for i := 0; i < s.len(); i++ {
		c := s.at(i)
		if c == '_' && !accept {
			return false
		}
		accept = isHex(rune(c))
	}
	return accept
}

func numPeriodsOK(s numberSpan) bool {
	period := false
	for i := 0; i < s.len(); i++ {
		c := s.at(i)
		if period && !isDigit(rune(c)) {
			return false
		}
		period = c == '.'
	}
	return !period
}

func integerValue(s numberSpan) (int64, error) {
	if !numUnderscoresOK(s) || numHasLeadingZero(s) {
		return 0, ErrSyntax
	}
	return parseInt(s, 0, 64)
}

func floatPreflight(s numberSpan) error {
	first, start := true, 0
	for i := 0; i <= s.len(); i++ {
		if i != s.len() {
			c := s.at(i)
			if c != '.' && c != 'e' && c != 'E' {
				continue
			}
		}
		if start < i {
			part := s.after(start).prefix(i - start)
			if !numUnderscoresOK(part) || first && numHasLeadingZero(part) {
				return ErrSyntax
			}
			first = false
		}
		start = i + 1
	}
	if !numPeriodsOK(s) {
		return ErrSyntax
	}
	return nil
}

// floatValue preserves Go 1.26.11's selection of the exact arithmetic,
// Eisel-Lemire and decimal fallback paths. A short normalised decimal would
// not preserve this selection for a long mantissa with a cancelling exponent.
func floatValue(s numberSpan) (float64, error) {
	if err := floatPreflight(s); err != nil {
		return 0, err
	}
	if s.equal("nan") || s.equal("+nan") || s.equal("-nan") {
		f := math.Float64frombits(0x7ff8000000000001)
		if s.equal("-nan") {
			f = math.Copysign(f, -1)
		}
		return f, nil
	}
	if s.equal("inf") || s.equal("+inf") {
		return math.Inf(1), nil
	}
	if s.equal("-inf") {
		return math.Inf(-1), nil
	}
	mantissa, exp, neg, trunc, hex, n, ok := readFloat(s)
	if !ok || hex || n != s.len() {
		return 0, ErrSyntax
	}
	if !trunc {
		if f, ok := atof64exact(mantissa, exp, neg); ok {
			return f, nil
		}
	}
	if f, ok := eiselLemire64(mantissa, exp, neg); ok {
		if !trunc {
			return f, nil
		}
		if upper, ok := eiselLemire64(mantissa+1, exp, neg); ok && f == upper {
			return f, nil
		}
	}
	var d decimal
	if !d.set(s) {
		return 0, ErrSyntax
	}
	bits, overflow := d.floatBits(&float64info)
	if overflow {
		return math.Float64frombits(bits), ErrNumberRange
	}
	return math.Float64frombits(bits), nil
}

// Native scalar conversion uses the pin's 64-bit type alone. The source
// of these layout constants is Go 1.26.11 src/internal/strconv/ftoa.go.
type floatInfo struct {
	mantbits uint
	expbits  uint
	bias     int
}

const (
	float64MantBits = 52
	float64ExpBits  = 11
	float64Bias     = -1023
)

var float64info = floatInfo{float64MantBits, float64ExpBits, float64Bias}

func float64frombits(bits uint64) float64 { return math.Float64frombits(bits) }
