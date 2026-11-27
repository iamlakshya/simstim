package secretbind

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// HeaderValidator shares the RFC token predicate with api and retains only
// the longest reserved exact field name, Transfer-Encoding (17 bytes).
type HeaderValidator struct {
	state     validationState
	prefix    [len("Transfer-Encoding")]byte
	used      int
	long      bool
	canonical bool
	upperNext bool
}

// NewHeaderValidator starts one cancellable credential-field validation.
func NewHeaderValidator(ctx context.Context) *HeaderValidator {
	var value HeaderValidator
	value.Reset(ctx)
	return &value
}

// Reset reuses one value-owned config-field state without retaining old input.
func (v *HeaderValidator) Reset(ctx context.Context) {
	*v = HeaderValidator{state: validationState{ctx: ctx}}
}

// NewCanonicalHeaderValidator requires the exact stored MIME field spelling.
// It shares the token/reserved grammar with the permissive config validator.
func NewCanonicalHeaderValidator(ctx context.Context) *HeaderValidator {
	var value HeaderValidator
	value.ResetCanonical(ctx)
	return &value
}

// ResetCanonical reuses the same grammar with strict stored MIME spelling.
func (v *HeaderValidator) ResetCanonical(ctx context.Context) {
	v.Reset(ctx)
	v.canonical, v.upperNext = true, true
}

// Write consumes decoded string bytes without retaining the input chunk.
func (v *HeaderValidator) Write(chunk []byte) (int, error) {
	if err := v.state.writing(); err != nil {
		return 0, err
	}
	for i, b := range chunk {
		if err := v.state.check(); err != nil {
			return i, err
		}
		if !api.ValidHTTPTokenByte(b) {
			return i, v.state.fail(errHeader)
		}
		if v.canonical {
			if v.upperNext && b >= 'a' && b <= 'z' || !v.upperNext && b >= 'A' && b <= 'Z' {
				return i, v.state.fail(errHeader)
			}
			v.upperNext = b == '-'
		}
		if v.used < len(v.prefix) {
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			v.prefix[v.used] = b
			v.used++
		} else {
			v.long = true
		}
	}
	return len(chunk), v.state.check()
}

// Finish validates the completed field name. Repeated calls are idempotent.
func (v *HeaderValidator) Finish() error {
	if err := v.state.check(); err != nil {
		return err
	}
	if v.state.finished {
		return nil
	}
	v.state.finished = true
	if v.used == 0 {
		return v.state.fail(errHeader)
	}
	field := string(v.prefix[:v.used])
	if strings.HasPrefix(field, "proxy-") || strings.HasPrefix(field, "x-forwarded-") {
		return v.state.fail(errHeader)
	}
	if !v.long {
		switch field {
		case "host", "content-length", "transfer-encoding", "trailer", "te", "connection", "upgrade", "keep-alive", "forwarded", "accept-encoding", "content-encoding", "content-type":
			return v.state.fail(errHeader)
		}
	}
	return nil
}

// Format keeps partial field names out of diagnostic output.
func (HeaderValidator) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
