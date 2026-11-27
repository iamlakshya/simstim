package secretbind

import (
	"context"
	"fmt"
	"io"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	rawFormat    = "{value}"
	bearerFormat = "Bearer {value}"
	basicPrefix  = "basic:"
	basicSuffix  = ":{value}"
	formatRaw    = 1
	formatBearer = 2
	formatBasic  = 3
)

// FormatValidator retains fixed grammar/UTF8 state and an eight-byte rolling
// suffix. It never keeps an unselected Basic username, regardless of length.
// Empty Basic usernames preserve the existing grammar. No new ban is inferred.
type FormatValidator struct {
	state       validationState
	mode        byte
	position    int
	tail        [len(basicSuffix)]byte
	tailStart   int
	tailUsed    int
	username    runeStream
	userBytes   uint64
	placeholder int
}

// NewFormatValidator starts one cancellable format validation.
func NewFormatValidator(ctx context.Context) *FormatValidator {
	var value FormatValidator
	value.Reset(ctx)
	return &value
}

// Reset reuses a value-owned format state and clears partial UTF8 and suffixes.
func (v *FormatValidator) Reset(ctx context.Context) {
	*v = FormatValidator{state: validationState{ctx: ctx}}
}

// Write consumes decoded string bytes without retaining the input chunk.
func (v *FormatValidator) Write(chunk []byte) (int, error) {
	if err := v.state.writing(); err != nil {
		return 0, err
	}
	for i, b := range chunk {
		if err := v.state.check(); err != nil {
			return i, err
		}
		if err := v.byte(b); err != nil {
			return i, v.state.fail(errFormat)
		}
	}
	return len(chunk), v.state.check()
}

func (v *FormatValidator) byte(b byte) error {
	if v.mode == 0 {
		switch b {
		case '{':
			v.mode = formatRaw
		case 'B':
			v.mode = formatBearer
		case 'b':
			v.mode = formatBasic
		default:
			return errFormat
		}
	}
	if v.mode == formatBasic && v.position == len(basicPrefix) {
		if v.tailUsed < len(v.tail) {
			v.tail[(v.tailStart+v.tailUsed)%len(v.tail)] = b
			v.tailUsed++
			return nil
		}
		userByte := v.tail[v.tailStart]
		v.tail[v.tailStart] = b
		v.tailStart = (v.tailStart + 1) % len(v.tail)
		if v.userBytes == ^uint64(0) {
			return errFormat
		}
		v.userBytes++
		if userByte == rawFormat[v.placeholder] {
			v.placeholder++
		} else if userByte == '{' {
			v.placeholder = 1
		} else {
			v.placeholder = 0
		}
		if v.placeholder == len(rawFormat) {
			return errFormat
		}
		return v.username.byte(userByte, func(r rune) error {
			if r == ':' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return errFormat
			}
			return nil
		})
	}
	expected := rawFormat
	if v.mode == formatBearer {
		expected = bearerFormat
	}
	if v.mode == formatBasic {
		expected = basicPrefix
	}
	if v.position >= len(expected) || expected[v.position] != b {
		return errFormat
	}
	v.position++
	return nil
}

// Finish validates the completed format. Repeated calls are idempotent.
func (v *FormatValidator) Finish() error {
	if err := v.state.check(); err != nil {
		return err
	}
	if v.state.finished {
		return nil
	}
	v.state.finished = true
	switch v.mode {
	case formatRaw:
		if v.position == len(rawFormat) {
			return nil
		}
	case formatBearer:
		if v.position == len(bearerFormat) {
			return nil
		}
	case formatBasic:
		if v.position != len(basicPrefix) || v.tailUsed != len(v.tail) {
			return v.state.fail(errFormat)
		}
		for i := range len(v.tail) {
			if v.tail[(v.tailStart+i)%len(v.tail)] != basicSuffix[i] {
				return v.state.fail(errFormat)
			}
		}
		if v.username.finish() == nil {
			return nil
		}
	}
	return v.state.fail(errFormat)
}

// FinishFacts returns only the validated format kind and Basic username byte
// count. It retains and returns no username text. Failed/reset states expose
// no usable facts, and the same Finish grammar owns all validation.
func (v *FormatValidator) FinishFacts() (kind string, userBytes uint64, err error) {
	if err := v.Finish(); err != nil {
		return "", 0, err
	}
	switch v.mode {
	case formatRaw:
		return "raw", 0, nil
	case formatBearer:
		return "bearer", 0, nil
	case formatBasic:
		return "basic", v.userBytes, nil
	default:
		return "", 0, errFormat
	}
}

// Format keeps partial username bytes out of diagnostic output.
func (FormatValidator) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
