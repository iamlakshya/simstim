package egress

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

var errCredentialField = errors.New("credential cannot use its HTTP field format")

// CredentialFieldValidator shares the full formatter's exact byte predicates
// and output length with borrowed FD text. It stores no value or username.
// It confers no binding, name, publication or local-review authority.
type CredentialFieldValidator struct {
	ctx              context.Context
	kind             byte
	userBytes, bytes uint64
	first, last      byte
	seen, padding    bool
	characters       bool
	finished         bool
	err              error
}

// Reset starts one value-owned field check from already validated format facts.
func (v *CredentialFieldValidator) Reset(ctx context.Context, kind string, userBytes uint64) {
	*v = CredentialFieldValidator{ctx: ctx, userBytes: userBytes}
	switch kind {
	case "raw":
		v.kind = 1
	case "bearer":
		v.kind = 2
	case "basic":
		v.kind = 3
	default:
		v.err = errCredentialField
	}
}

func (v *CredentialFieldValidator) check() error {
	if v.ctx == nil {
		return errCredentialField
	}
	if err := v.ctx.Err(); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return err
		}
		return errCredentialField
	}
	return v.err
}

// Write checks exact decoded value bytes without retaining the input chunk.
func (v *CredentialFieldValidator) Write(input []byte) (int, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	if v.finished {
		return 0, errCredentialField
	}
	for i, b := range input {
		if err := v.byte(b); err != nil {
			return i, err
		}
	}
	return len(input), v.check()
}

// WriteString checks the same bytes without copying an owned full value.
// It retains neither the string nor a view into its storage.
func (v *CredentialFieldValidator) WriteString(input string) (int, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	if v.finished {
		return 0, errCredentialField
	}
	for i := range len(input) {
		if err := v.byte(input[i]); err != nil {
			return i, err
		}
	}
	return len(input), v.check()
}

func (v *CredentialFieldValidator) byte(b byte) error {
	if err := v.check(); err != nil {
		return err
	}
	if v.bytes == ^uint64(0) {
		v.err = errCredentialField
		return v.err
	}
	v.bytes++
	if !v.seen {
		v.first = b
		v.seen = true
	}
	v.last = b
	switch v.kind {
	case 1:
		if b < 0x20 && b != '\t' || b == 0x7f {
			v.err = errCredentialField
			return v.err
		}
	case 2:
		if b == '=' {
			v.padding = true
			return nil
		}
		valid := b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
		switch b {
		case '-', '.', '_', '~', '+', '/':
			valid = true
		}
		if v.padding || !valid {
			v.err = errCredentialField
			return v.err
		}
		v.characters = true
	case 3:
		// Basic encodes these bytes; caller value/shape restrictions stay separate.
	}
	return nil
}

// Finish returns the exact formatted byte count, including its scheme prefix.
// Repeated successful calls are idempotent. No formatted field is produced.
func (v *CredentialFieldValidator) Finish() (uint64, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	v.finished = true
	switch v.kind {
	case 1:
		if v.seen && (v.first == ' ' || v.first == '\t' || v.last == ' ' || v.last == '\t') {
			v.err = errCredentialField
			return 0, v.err
		}
		return v.bytes, nil
	case 2:
		if !v.characters || v.bytes > ^uint64(0)-7 {
			v.err = errCredentialField
			return 0, v.err
		}
		return v.bytes + 7, nil
	case 3:
		if v.userBytes == ^uint64(0) || v.bytes > ^uint64(0)-v.userBytes-1 {
			v.err = errCredentialField
			return 0, v.err
		}
		plain := v.userBytes + 1 + v.bytes
		groups := plain / 3
		if plain%3 != 0 {
			groups++
		}
		if groups > (^uint64(0)-6)/4 {
			v.err = errCredentialField
			return 0, v.err
		}
		return 6 + groups*4, nil
	default:
		return 0, errCredentialField
	}
}

// Format keeps partial validation state out of diagnostic output.
func (CredentialFieldValidator) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
