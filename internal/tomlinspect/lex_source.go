package tomlinspect

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

type item struct {
	typ    itemType
	val    Span
	err    error
	pos    Position
	parent byte
}

type lexer struct {
	input      *source
	base       int
	start, pos int
	line       int
	state      stateFn
	items      [10]item
	first, n   int
	esc        bool
	prevWidths [4]int
	nprev      int
	atEOF      bool
	arena      *lexStack
	steps      uint64
	tiny       bool
	tinyData   [8]byte
	tinyDepth  int
}

func lex(input *source, arena *lexStack) *lexer {
	return resetLexer(new(lexer), input, arena)
}

func resetLexer(lx *lexer, input *source, arena *lexStack) *lexer {
	*lx = lexer{input: input, arena: arena, line: 1, state: lexTop}
	if lx.prefix("\xff\xfe") || lx.prefix("\xfe\xff") {
		lx.base = 2
	} else if lx.prefix("\xef\xbb\xbf") {
		lx.base = 3
	}
	for i := range min(6, lx.length()) {
		if lx.byteAt(i) == 0 {
			panic(&Error{Position: Position{Line: 1, Col: 1, Start: i, Len: 1}, Cause: ErrSyntax})
		}
	}
	return lx
}

func (lx *lexer) prefix(value string) bool {
	if int64(len(value)) > lx.input.size {
		return false
	}
	for i := range len(value) {
		if lx.byteAt(i) != value[i] {
			return false
		}
	}
	return true
}

func (lx *lexer) length() int { return int(lx.input.size) - lx.base }

func (lx *lexer) byteAt(pos int) byte {
	b, err := lx.input.at(int64(lx.base + pos))
	if err != nil {
		panic(&Error{Position: lx.getPos(), Cause: err})
	}
	return b
}

func (lx *lexer) current() Span {
	return Span{Start: int64(lx.base + lx.start), End: int64(lx.base + lx.pos)}
}

func (lx *lexer) currentSuffix(value string) bool {
	yes, err := lx.input.suffix(lx.current(), value)
	if err != nil {
		panic(&Error{Position: lx.getPos(), Cause: err})
	}
	return yes
}

func (lx *lexer) getPos() Position {
	p := Position{Line: lx.line, Start: lx.start, Len: lx.pos - lx.start}
	if p.Len <= 0 {
		p.Len = 1
	}
	return p
}

func (lx *lexer) queue(value item) {
	if lx.n == len(lx.items) {
		panic("pinned lexer emission invariant exceeded")
	}
	lx.items[(lx.first+lx.n)%len(lx.items)] = value
	lx.n++
}

func (lx *lexer) nextItem() item {
	for lx.n == 0 {
		if err := lx.input.ctx.Err(); err != nil {
			return item{typ: itemError, err: err, pos: lx.getPos()}
		}
		if lx.state == nil {
			return item{typ: itemEOF, pos: lx.getPos()}
		}
		lx.steps++
		lx.state = lx.state(lx)
	}
	out := lx.items[lx.first]
	lx.items[lx.first] = item{}
	lx.first, lx.n = (lx.first+1)%len(lx.items), lx.n-1
	return out
}

func (lx *lexer) emit(kind itemType) {
	if lx.start > lx.pos {
		lx.error(errLexUTF8{lx.byteAt(lx.pos)})
		return
	}
	lx.queue(item{typ: kind, pos: lx.getPos(), val: lx.current(), parent: lx.peekContinuation()})
	lx.start = lx.pos
}

func (lx *lexer) emitTrim(kind itemType) {
	span := lx.current()
	for span.Start < span.End {
		r, width, err := lx.input.runeAt(span.Start, span.End)
		if err != nil {
			panic(&Error{Position: lx.getPos(), Cause: err})
		}
		if !unicode.IsSpace(r) {
			break
		}
		span.Start += int64(width)
	}
	for span.End > span.Start {
		start := span.End - 1
		for start > span.Start && lx.byteAt(int(start)-lx.base)&0xc0 == 0x80 {
			start--
		}
		r, _, err := lx.input.runeAt(start, span.End)
		if err != nil {
			panic(&Error{Position: lx.getPos(), Cause: err})
		}
		if !unicode.IsSpace(r) {
			break
		}
		span.End = start
	}
	lx.queue(item{typ: kind, pos: lx.getPos(), val: span, parent: lx.peekContinuation()})
	lx.start = lx.pos
}

func (lx *lexer) next() rune {
	if lx.atEOF {
		panic("pinned lexer next after EOF")
	}
	if lx.pos >= lx.length() {
		lx.atEOF = true
		return eof
	}
	if lx.byteAt(lx.pos) == '\n' {
		lx.line++
	}
	lx.prevWidths[3], lx.prevWidths[2], lx.prevWidths[1] = lx.prevWidths[2], lx.prevWidths[1], lx.prevWidths[0]
	lx.nprev = min(4, lx.nprev+1)
	r, width, err := lx.input.runeAt(int64(lx.base+lx.pos), lx.input.size)
	if err != nil {
		if err == ErrUTF8 {
			lx.error(errLexUTF8{lx.byteAt(lx.pos)})
			return utf8.RuneError
		}
		panic(&Error{Position: lx.getPos(), Cause: err})
	}
	if isControl(r) || r == '\r' && (lx.length()-1 == lx.pos || lx.byteAt(lx.pos+1) != '\n') {
		lx.errorControlChar(r)
		return utf8.RuneError
	}
	lx.prevWidths[0], lx.pos = width, lx.pos+width
	return r
}

func (lx *lexer) ignore() { lx.start = lx.pos }

func (lx *lexer) backup() {
	if lx.atEOF {
		lx.atEOF = false
		return
	}
	if lx.nprev == 0 {
		panic("pinned lexer backup invariant")
	}
	w := lx.prevWidths[0]
	lx.prevWidths[0], lx.prevWidths[1], lx.prevWidths[2] = lx.prevWidths[1], lx.prevWidths[2], lx.prevWidths[3]
	lx.nprev--
	lx.pos -= w
	if lx.pos < lx.length() && lx.byteAt(lx.pos) == '\n' {
		lx.line--
	}
}

func (lx *lexer) accept(want rune) bool {
	if lx.next() == want {
		return true
	}
	lx.backup()
	return false
}

func (lx *lexer) peek() rune { r := lx.next(); lx.backup(); return r }

func (lx *lexer) skip(pred func(rune) bool) {
	for {
		if pred(lx.next()) {
			continue
		}
		lx.backup()
		lx.ignore()
		return
	}
}

func (lx *lexer) error(err error) stateFn {
	if lx.atEOF {
		return lx.errorPrevLine(err)
	}
	lx.queue(item{typ: itemError, err: err, pos: lx.getPos()})
	return nil
}

func (lx *lexer) errorPrevLine(err error) stateFn {
	p := lx.getPos()
	p.Line, p.Len, p.Start = p.Line-1, 1, lx.pos-1
	lx.queue(item{typ: itemError, err: err, pos: p})
	return nil
}

func (lx *lexer) errorPos(start, length int, err error) stateFn {
	p := lx.getPos()
	p.Start, p.Len = start, length
	lx.queue(item{typ: itemError, err: err, pos: p})
	return nil
}

func (lx *lexer) errorf(format string, values ...any) stateFn {
	p := lx.getPos()
	if lx.atEOF {
		if lx.pos > 0 && lx.byteAt(lx.pos-1) == '\n' {
			p.Line--
		}
		p.Len, p.Start = 1, lx.pos-1
	}
	lx.queue(item{typ: itemError, err: fmt.Errorf(format, values...), pos: p})
	return nil
}

func (lx *lexer) errorControlChar(r rune) stateFn {
	return lx.errorPos(lx.pos-1, 1, errLexControl{r})
}

// redactedSpanDiagnostic projects only an error argument's TYPE. The lexer
// already retains its exact Position separately; no token/command bytes or
// span fields are formatted, copied or logged through the diagnostic.
func redactedSpanDiagnostic(_ Span) string { return "<redacted TOML token>" }
