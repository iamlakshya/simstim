// Package tomlinspect extends the pinned BurntSushi/toml grammar with
// FD-backed validation cursors. See PINNED-SOURCE.md and COPYING.
package tomlinspect

import (
	"context"
	"errors"
	"io"
	"unicode/utf8"
)

// MaxInput is the existing C9 config-file limit, not a new projection cap.
const MaxInput = 1 << 20

var (
	// ErrInputSize refuses an invalid declared size or the existing config limit.
	ErrInputSize = errors.New("TOML input exceeds the existing config limit")
	// ErrSyntax is a pinned TOML syntax, duplicate or object-transition refusal.
	ErrSyntax = errors.New("invalid pinned TOML syntax")
	// ErrUTF8 distinguishes invalid raw/escaped Unicode from literal U+FFFD.
	ErrUTF8 = errors.New("invalid raw UTF8 in TOML")
)

type source struct {
	generation       uint64
	ctx              context.Context
	input            io.ReaderAt
	size             int64
	clock            uint64
	reads, readBytes uint64
	windows          [4]sourceWindow
}

type sourceWindow struct {
	start int64
	n     int
	used  uint64
	data  [4096]byte
}

func newSource(ctx context.Context, input io.ReaderAt, size int64) (*source, error) {
	if ctx == nil || input == nil || size < 0 || size > MaxInput {
		return nil, ErrInputSize
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &source{ctx: ctx, input: input, size: size}, nil
}

func (s *source) at(offset int64) (byte, error) {
	if s == nil || s.ctx == nil || s.input == nil {
		return 0, ErrIndexInvariant
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 || offset >= s.size {
		return 0, io.EOF
	}
	var replace *sourceWindow
	for i := range s.windows {
		w := &s.windows[i]
		if w.n > 0 && offset >= w.start && offset < w.start+int64(w.n) {
			s.clock++
			w.used = s.clock
			return w.data[offset-w.start], nil
		}
		if replace == nil || w.n == 0 || w.used < replace.used {
			replace = w
		}
	}
	w := replace
	w.start, w.n = offset, 0
	want := int(min(int64(len(w.data)), s.size-offset))
	n, err := s.input.ReadAt(w.data[:want], offset)
	s.reads++
	if n > 0 {
		s.readBytes += uint64(n)
	}
	if err != nil && err != io.EOF {
		return 0, err
	}
	if n == 0 {
		if err == nil {
			err = io.ErrNoProgress
		}
		return 0, err
	}
	w.n = n
	s.clock++
	w.used = s.clock
	return w.data[0], nil
}

func (s *source) runeAt(offset, end int64) (rune, int, error) {
	if offset >= end {
		return 0, 0, io.EOF
	}
	var encoded [utf8.UTFMax]byte
	n := int(min(int64(len(encoded)), end-offset))
	for i := range n {
		b, err := s.at(offset + int64(i))
		if err != nil {
			return 0, 0, err
		}
		encoded[i] = b
	}
	r, width := utf8.DecodeRune(encoded[:n])
	if r == utf8.RuneError && width == 1 {
		return 0, 0, ErrUTF8
	}
	return r, width, nil
}

// Span is an exact source extent. It owns no input bytes or decoded value.
type Span struct{ Start, End int64 }

func (s *source) spanValid(span Span) error {
	if s == nil || s.ctx == nil || s.input == nil {
		return ErrIndexInvariant
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if span.Start < 0 || span.End < span.Start || span.End > s.size {
		return ErrSyntax
	}
	return nil
}

func (s *source) suffix(span Span, suffix string) (bool, error) {
	if err := s.spanValid(span); err != nil {
		return false, err
	}
	if span.End-span.Start < int64(len(suffix)) {
		return false, nil
	}
	start := span.End - int64(len(suffix))
	for i := range len(suffix) {
		b, err := s.at(start + int64(i))
		if err != nil || b != suffix[i] {
			return false, err
		}
	}
	return true, nil
}
