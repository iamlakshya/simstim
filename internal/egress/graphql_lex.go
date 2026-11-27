package egress

import (
	"context"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const gqlDepth = 10000 // SAME existing encoding/json inspection syntax boundary.

type gqlToken struct {
	kind byte
	text string
}
type gqlLexer struct {
	ctx    context.Context
	source string
	at     int
	token  gqlToken
	ready  bool
}

func gqlNameStart(b byte) bool { return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }
func gqlNameByte(b byte) bool  { return gqlNameStart(b) || b >= '0' && b <= '9' }

func (l *gqlLexer) peek() (gqlToken, error) {
	if !l.ready {
		token, err := l.scan()
		if err != nil {
			return token, err
		}
		l.token, l.ready = token, true
	}
	return l.token, nil
}
func (l *gqlLexer) take() (gqlToken, error) {
	token, err := l.peek()
	l.ready = false
	return token, err
}
func (l *gqlLexer) accept(kind byte) bool {
	token, err := l.peek()
	if err == nil && token.kind == kind {
		_, _ = l.take()
		return true
	}
	return false
}
func (l *gqlLexer) expect(kind byte) (gqlToken, error) {
	token, err := l.take()
	if err != nil || token.kind != kind {
		return token, errServiceShape
	}
	return token, nil
}

func (l *gqlLexer) scan() (gqlToken, error) {
	if l.ctx == nil {
		return gqlToken{}, errServiceShape
	}
	if err := l.ctx.Err(); err != nil {
		return gqlToken{}, err
	}
	for l.at < len(l.source) {
		if err := l.ctx.Err(); err != nil {
			return gqlToken{}, err
		}
		b := l.source[l.at]
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == ',' {
			l.at++
			continue
		}
		if strings.HasPrefix(l.source[l.at:], "\xef\xbb\xbf") {
			l.at += 3
			continue
		}
		if b == '#' {
			for l.at < len(l.source) && l.source[l.at] != '\r' && l.source[l.at] != '\n' {
				l.at++
			}
			continue
		}
		break
	}
	if l.at == len(l.source) {
		return gqlToken{}, nil
	}
	start := l.at
	b := l.source[l.at]
	l.at++
	if gqlNameStart(b) {
		for l.at < len(l.source) && gqlNameByte(l.source[l.at]) {
			l.at++
		}
		return gqlToken{kind: 'N', text: l.source[start:l.at]}, nil
	}
	if b == '"' {
		if strings.HasPrefix(l.source[start:], `"""`) {
			return l.blockString(start)
		}
		return l.quotedString()
	}
	if b == '-' || b >= '0' && b <= '9' {
		if b == '-' {
			if l.at == len(l.source) || l.source[l.at] < '0' || l.source[l.at] > '9' {
				return gqlToken{}, errServiceShape
			}
			b = l.source[l.at]
			l.at++
		}
		if b == '0' {
			if l.at < len(l.source) && l.source[l.at] >= '0' && l.source[l.at] <= '9' {
				return gqlToken{}, errServiceShape
			}
		} else {
			for l.at < len(l.source) && l.source[l.at] >= '0' && l.source[l.at] <= '9' {
				l.at++
			}
		}
		kind := byte('I')
		for _, marker := range []byte{'.', 'e'} {
			if l.at == len(l.source) {
				break
			}
			c := l.source[l.at]
			if marker == '.' && c != '.' || marker == 'e' && c != 'e' && c != 'E' {
				continue
			}
			kind = 'F'
			l.at++
			if marker == 'e' && l.at < len(l.source) && (l.source[l.at] == '+' || l.source[l.at] == '-') {
				l.at++
			}
			first := l.at
			for l.at < len(l.source) && l.source[l.at] >= '0' && l.source[l.at] <= '9' {
				l.at++
			}
			if first == l.at {
				return gqlToken{}, errServiceShape
			}
		}
		if l.at < len(l.source) && (gqlNameStart(l.source[l.at]) || l.source[l.at] == '.') {
			return gqlToken{}, errServiceShape
		}
		return gqlToken{kind: kind, text: l.source[start:l.at]}, nil
	}
	if b == '.' {
		if !strings.HasPrefix(l.source[start:], "...") {
			return gqlToken{}, errServiceShape
		}
		l.at = start + 3
		return gqlToken{kind: '.'}, nil
	}
	if strings.ContainsRune("!$&():=@[]{|}", rune(b)) {
		return gqlToken{kind: b}, nil
	}
	return gqlToken{}, errServiceShape
}

func (l *gqlLexer) blockString(start int) (gqlToken, error) {
	l.at = start + 3
	var text strings.Builder
	for l.at < len(l.source) {
		if err := l.ctx.Err(); err != nil {
			return gqlToken{}, err
		}
		if strings.HasPrefix(l.source[l.at:], `\"""`) {
			text.WriteString(`"""`)
			l.at += 4
			continue
		}
		if strings.HasPrefix(l.source[l.at:], `"""`) {
			l.at += 3
			return gqlToken{kind: 'S', text: normaliseGQLBlock(text.String())}, nil
		}
		r, width := utf8.DecodeRuneInString(l.source[l.at:])
		if r == utf8.RuneError && width == 1 || r < 0x20 && r != '\t' && r != '\r' && r != '\n' {
			return gqlToken{}, errServiceShape
		}
		text.WriteRune(r)
		l.at += width
	}
	return gqlToken{}, errServiceShape
}

func normaliseGQLBlock(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(value, "\n")
	indent := -1
	for i, line := range lines {
		if i == 0 {
			continue
		}
		width := len(line) - len(strings.TrimLeft(line, " \t"))
		if width < len(line) && (indent < 0 || width < indent) {
			indent = width
		}
	}
	if indent > 0 {
		for i := 1; i < len(lines); i++ {
			if len(lines[i]) >= indent {
				lines[i] = lines[i][indent:]
			}
		}
	}
	blank := func(line string) bool { return strings.Trim(line, " \t") == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func gqlHex4(source string, at int) (uint16, bool) {
	if at < 0 || len(source)-at < 4 {
		return 0, false
	}
	var value uint16
	for i := 0; i < 4; i++ {
		b := source[at+i]
		var digit byte
		switch {
		case b >= '0' && b <= '9':
			digit = b - '0'
		case b >= 'a' && b <= 'f':
			digit = b - 'a' + 10
		case b >= 'A' && b <= 'F':
			digit = b - 'A' + 10
		default:
			return 0, false
		}
		value = value*16 + uint16(digit)
	}
	return value, true
}

func (l *gqlLexer) quotedString() (gqlToken, error) {
	var value strings.Builder
	for l.at < len(l.source) {
		if err := l.ctx.Err(); err != nil {
			return gqlToken{}, err
		}
		b := l.source[l.at]
		l.at++
		if b == '"' {
			return gqlToken{kind: 'S', text: value.String()}, nil
		}
		if b < 0x20 {
			return gqlToken{}, errServiceShape
		}
		if b != '\\' {
			l.at--
			r, width := utf8.DecodeRuneInString(l.source[l.at:])
			if r == utf8.RuneError && width == 1 {
				return gqlToken{}, errServiceShape
			}
			value.WriteRune(r)
			l.at += width
			continue
		}
		if l.at == len(l.source) {
			return gqlToken{}, errServiceShape
		}
		escape := l.source[l.at]
		l.at++
		switch escape {
		case '"', '\\', '/':
			value.WriteByte(escape)
		case 'b':
			value.WriteByte('\b')
		case 'f':
			value.WriteByte('\f')
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case 'u':
			if l.at < len(l.source) && l.source[l.at] == '{' {
				l.at++
				start := l.at
				var scalar uint32
				for l.at < len(l.source) && l.source[l.at] != '}' {
					digit, ok := gqlHexDigit(l.source[l.at])
					if !ok || l.at-start >= 6 {
						return gqlToken{}, errServiceShape
					}
					scalar = scalar*16 + uint32(digit)
					l.at++
				}
				if l.at == len(l.source) || l.at == start || scalar > utf8.MaxRune || scalar >= 0xd800 && scalar <= 0xdfff {
					return gqlToken{}, errServiceShape
				}
				l.at++
				value.WriteRune(rune(scalar))
				continue
			}
			code, ok := gqlHex4(l.source, l.at)
			if !ok {
				return gqlToken{}, errServiceShape
			}
			l.at += 4
			if code >= 0xd800 && code <= 0xdbff {
				if !strings.HasPrefix(l.source[l.at:], `\u`) {
					return gqlToken{}, errServiceShape
				}
				low, ok := gqlHex4(l.source, l.at+2)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return gqlToken{}, errServiceShape
				}
				l.at += 6
				value.WriteRune(utf16.DecodeRune(rune(code), rune(low)))
			} else {
				if code >= 0xdc00 && code <= 0xdfff {
					return gqlToken{}, errServiceShape
				}
				value.WriteRune(rune(code))
			}
		default:
			return gqlToken{}, errServiceShape
		}
	}
	return gqlToken{}, errServiceShape
}

func gqlHexDigit(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}
