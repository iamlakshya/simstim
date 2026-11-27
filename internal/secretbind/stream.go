package secretbind

import (
	"context"
	"errors"
	"unicode/utf8"
)

var (
	errHeader    = errors.New("secret binding needs one credential header, without routing or framing fields")
	errFormat    = errors.New("secret format must be {value}, Bearer {value}, or basic:<username>:{value}")
	errAuthority = errors.New("secret binding needs an exact literal HTTPS authority on port 443")
	errFinished  = errors.New("secret binding validator is already finished")
	errUTF8      = errors.New("secret binding contains invalid UTF8")
)

type validationState struct {
	ctx      context.Context
	err      error
	finished bool
}

func (s *validationState) check() error {
	if s.ctx == nil {
		return errors.New("secret binding validator needs a context")
	}
	if err := s.ctx.Err(); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return err
		}
		return errors.New("secret binding validation was cancelled")
	}
	return s.err
}

func (s *validationState) writing() error {
	if err := s.check(); err != nil {
		return err
	}
	if s.finished {
		return errFinished
	}
	return nil
}

func (s *validationState) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// runeStream retains at most one incomplete encoded scalar. It never keeps
// the source string and never replaces malformed input with a new codepoint.
type runeStream struct {
	buffer [utf8.UTFMax]byte
	used   int
}

func (s *runeStream) byte(b byte, accept func(rune) error) error {
	if s.used == 0 && b < utf8.RuneSelf {
		return accept(rune(b))
	}
	s.buffer[s.used] = b
	s.used++
	if !utf8.FullRune(s.buffer[:s.used]) {
		return nil
	}
	r, width := utf8.DecodeRune(s.buffer[:s.used])
	if width != s.used || r == utf8.RuneError && width == 1 {
		return errUTF8
	}
	s.used = 0
	return accept(r)
}

func (s *runeStream) finish() error {
	if s.used != 0 {
		return errUTF8
	}
	return nil
}
