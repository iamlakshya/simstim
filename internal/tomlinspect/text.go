package tomlinspect

import (
	"io"
	"unicode"
	"unicode/utf8"
)

// Text is a borrowed decoded cursor over a pinned lexer token. It retains
// only offsets and the pinned token kind, never a decoded command or key.
type Text struct {
	generation uint64
	source     *source
	span       Span
	kind       itemType
}

type textIterator struct {
	text           Text
	at             int64
	multi, escaped bool
}

func (t Text) iterator() (textIterator, error) {
	i := textIterator{text: t, at: t.span.Start, multi: t.kind == itemMultilineString || t.kind == itemRawMultilineString, escaped: t.kind == itemStringEsc || t.kind == itemMultilineString}
	if t.source == nil || t.generation != t.source.generation {
		return i, ErrIndexInvariant
	}
	if err := t.source.spanValid(t.span); err != nil {
		return i, err
	}
	if i.multi && i.at < t.span.End {
		b, err := t.source.at(i.at)
		if err != nil {
			return i, err
		}
		if b == '\n' {
			i.at++
		} else if b == '\r' && i.at+1 < t.span.End {
			b, err = t.source.at(i.at + 1)
			if err != nil {
				return i, err
			}
			if b == '\n' {
				i.at += 2
			}
		}
	}
	return i, nil
}

func (i *textIterator) next() (rune, error) {
	for {
		if i.text.source == nil || i.text.source.ctx == nil || i.text.generation != i.text.source.generation {
			return 0, ErrIndexInvariant
		}
		if err := i.text.source.ctx.Err(); err != nil {
			return 0, err
		}
		if i.at == i.text.span.End {
			return 0, io.EOF
		}
		r, width, err := i.text.source.runeAt(i.at, i.text.span.End)
		if err != nil {
			return 0, err
		}
		i.at += int64(width)
		if r != '\\' || !i.escaped {
			return r, nil
		}
		if i.multi {
			end, newline, err := i.text.continuation(i.at)
			if err != nil {
				return 0, err
			}
			if newline {
				i.at = end
				continue
			}
		}
		r, width, err = i.text.source.runeAt(i.at, i.text.span.End)
		if err != nil {
			return 0, ErrSyntax
		}
		i.at += int64(width)
		switch r {
		case 'b':
			return '\b', nil
		case 't':
			return '\t', nil
		case 'n':
			return '\n', nil
		case 'f':
			return '\f', nil
		case 'r':
			return '\r', nil
		case 'e':
			return 0x1b, nil
		case '"', '\\':
			return r, nil
		case 'x', 'u', 'U':
			count := 2
			if r == 'u' {
				count = 4
			} else if r == 'U' {
				count = 8
			}
			r = 0
			for range count {
				if i.at >= i.text.span.End {
					return 0, ErrSyntax
				}
				b, err := i.text.source.at(i.at)
				if err != nil {
					return 0, err
				}
				i.at++
				digit := -1
				if b >= '0' && b <= '9' {
					digit = int(b - '0')
				} else if b|0x20 >= 'a' && b|0x20 <= 'f' {
					digit = int((b|0x20)-'a') + 10
				}
				if digit < 0 {
					return 0, ErrSyntax
				}
				r = r<<4 | rune(digit)
			}
			if !utf8.ValidRune(r) {
				return 0, ErrUTF8
			}
			return r, nil
		default:
			return 0, ErrSyntax
		}
	}
}

// Walk streams the exact pinned decoded runes without retaining a string.
func (t Text) Walk(visit func(rune) error) error {
	i, err := t.iterator()
	if err != nil {
		return err
	}
	for {
		r, err := i.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(r); err != nil {
			return err
		}
	}
}

func (a Text) equalText(b Text) (bool, error) {
	x, err := a.iterator()
	if err != nil {
		return false, err
	}
	y, err := b.iterator()
	if err != nil {
		return false, err
	}
	for {
		r, re := x.next()
		s, se := y.next()
		if re != nil && re != io.EOF {
			return false, re
		}
		if se != nil && se != io.EOF {
			return false, se
		}
		if re == io.EOF || se == io.EOF {
			return re == se, nil
		}
		if r != s {
			return false, nil
		}
	}
}

func (t Text) continuation(at int64) (int64, bool, error) {
	newline := false
	for at < t.span.End {
		b, err := t.source.at(at)
		if err != nil {
			return at, false, err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			break
		}
		newline = newline || b == '\n'
		at++
	}
	return at, newline, nil
}

// Equal compares exact decoded text with a code-owned literal without an
// owned string. The complete token was validated by the native parser.
func (t Text) Equal(want string) (bool, error) {
	return t.equalLiteral(want, false)
}

// EqualFold uses the same Unicode simple-fold equivalence as strings.EqualFold.
func (t Text) EqualFold(want string) (bool, error) {
	return t.equalLiteral(want, true)
}

func (t Text) equalLiteral(want string, fold bool) (bool, error) {
	cursor, err := t.iterator()
	if err != nil {
		return false, err
	}
	for len(want) > 0 {
		actual, err := cursor.next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		expected, width := utf8.DecodeRuneInString(want)
		want = want[width:]
		if actual == expected {
			continue
		}
		if !fold {
			return false, nil
		}
		equal := false
		for next := unicode.SimpleFold(actual); next != actual; next = unicode.SimpleFold(next) {
			if next == expected {
				equal = true
				break
			}
		}
		if !equal {
			return false, nil
		}
	}
	_, err = cursor.next()
	if err == io.EOF {
		return true, nil
	}
	return false, err
}
