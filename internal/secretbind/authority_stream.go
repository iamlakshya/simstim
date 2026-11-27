package secretbind

import (
	"context"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

// This bound follows nettarget.NormaliseHost: 253 canonical ASCII DNS bytes,
// one optional trailing dot, then the only accepted explicit port, :443.
// IPv4/IPv6 literals are shorter. TrimSpace padding never enters the buffer.
const maxBindingAuthorityBytes = 253 + 1 + len(":443")

// AuthorityValidator retains a bounded literal while streaming arbitrary
// leading/trailing Unicode trim padding. Normalisation belongs to nettarget.
type AuthorityValidator struct {
	state     validationState
	runes     runeStream
	literal   [maxBindingAuthorityBytes]byte
	used      int
	started   bool
	space     bool
	canonical string
	target    nettarget.Target
	strict    bool
}

// NewAuthorityValidator starts one cancellable HTTPS authority validation.
func NewAuthorityValidator(ctx context.Context) *AuthorityValidator {
	var value AuthorityValidator
	value.Reset(ctx)
	return &value
}

// Reset reuses one bounded value state and releases prior public descriptors.
func (v *AuthorityValidator) Reset(ctx context.Context) {
	*v = AuthorityValidator{state: validationState{ctx: ctx}}
}

// NewCanonicalAuthorityValidator requires exact stored host:443 spelling.
// Config normalisation remains available through NewAuthorityValidator.
func NewCanonicalAuthorityValidator(ctx context.Context) *AuthorityValidator {
	var value AuthorityValidator
	value.ResetCanonical(ctx)
	return &value
}

// ResetCanonical reuses the same grammar with exact stored host:443 spelling.
func (v *AuthorityValidator) ResetCanonical(ctx context.Context) {
	v.Reset(ctx)
	v.strict = true
}

// Write consumes decoded string bytes without retaining the input chunk.
func (v *AuthorityValidator) Write(chunk []byte) (int, error) {
	if err := v.state.writing(); err != nil {
		return 0, err
	}
	for i, b := range chunk {
		if err := v.state.check(); err != nil {
			return i, err
		}
		if err := v.runes.byte(b, v.rune); err != nil {
			return i, v.state.fail(errAuthority)
		}
	}
	return len(chunk), v.state.check()
}

func (v *AuthorityValidator) rune(r rune) error {
	if unicode.IsSpace(r) {
		if v.strict {
			return errAuthority
		}
		if v.started {
			v.space = true
		}
		return nil
	}
	if v.space {
		return errAuthority
	}
	v.started = true
	width := utf8.RuneLen(r)
	if width > len(v.literal)-v.used {
		return errAuthority
	}
	v.used = len(utf8.AppendRune(v.literal[:v.used], r))
	return nil
}

// Finish returns the canonical HTTPS authority. Repeated calls are idempotent.
func (v *AuthorityValidator) Finish() (string, error) {
	if err := v.FinishCheck(); err != nil {
		return "", err
	}
	if v.canonical == "" {
		v.canonical = materialiseBindingAuthority(v.target)
	}
	return v.canonical, nil
}

// FinishCheck validates without materialising the final canonical authority
// string in config mode. Shared nettarget parsing can still allocate bounded
// scratch; this method does not claim allocation-free authority admission.
func (v *AuthorityValidator) FinishCheck() error {
	if err := v.state.check(); err != nil {
		return err
	}
	if v.state.finished {
		return nil
	}
	v.state.finished = true
	if v.runes.finish() != nil || v.used == 0 {
		return v.state.fail(errAuthority)
	}
	target, err := parseBindingAuthority(string(v.literal[:v.used]))
	if err != nil {
		return v.state.fail(err)
	}
	v.target = target
	if v.strict {
		v.canonical = materialiseBindingAuthority(target)
		if v.canonical != string(v.literal[:v.used]) {
			return v.state.fail(errAuthority)
		}
	}
	return nil
}

// Format keeps partial authority bytes out of diagnostic output.
func (AuthorityValidator) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
