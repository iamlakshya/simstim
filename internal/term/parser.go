// Package term understands just enough of a terminal byte stream to keep the
// user's terminal clean: it tracks the modes a session turns on, builds the
// sequences that reset or restore them, and removes terminal queries from
// replayed history. It never changes live bytes.
package term

// Kind is the kind of a token.
type Kind int

// Token kinds.
const (
	Text   Kind = iota // printable text and C0 controls
	CSI                // ESC [ … final
	OSC                // ESC ] … BEL or ST
	DCS                // ESC P … ST
	StrSeq             // APC, PM or SOS: ESC _ / ESC ^ / ESC X … ST
	Esc                // other two- or three-byte ESC sequences
)

// Token is one complete piece of the stream.
type Token struct {
	Kind Kind
	Raw  []byte // the exact bytes, including introducer and terminator
	// For CSI:
	Prefix       byte   // '?', '>', '<' or '=' (0 if none)
	Params       []byte // parameter bytes after the prefix
	Intermediate []byte
	Final        byte
	// For OSC, DCS and string sequences: the payload without introducer and terminator.
	Payload []byte
}

// Parser splits a stream into tokens. It keeps incomplete sequences across calls.
type Parser struct {
	buf []byte
}

const maxSeq = 64 << 10 // a sequence longer than this is passed through as text

// Feed adds p and calls fn for every complete token. Incomplete sequences stay
// buffered until the next call.
func (ps *Parser) Feed(p []byte, fn func(Token)) {
	ps.buf = append(ps.buf, p...)
	b := ps.buf
	i := 0
	textStart := 0
	flushText := func(end int) {
		if end > textStart {
			fn(Token{Kind: Text, Raw: b[textStart:end]})
		}
	}
	for i < len(b) {
		if b[i] != 0x1b {
			i++
			continue
		}
		n, tok, ok := parseEsc(b[i:])
		if !ok {
			if len(b)-i > maxSeq {
				// Give up on a runaway sequence: treat the ESC as text.
				i++
				continue
			}
			break // incomplete; keep it for the next call
		}
		flushText(i)
		fn(tok)
		i += n
		textStart = i
	}
	if i >= len(b) {
		flushText(len(b))
		ps.buf = ps.buf[:0]
		return
	}
	flushText(i)
	rest := append([]byte(nil), b[i:]...)
	ps.buf = append(ps.buf[:0], rest...)
}

// Pending reports whether an incomplete sequence is buffered.
func (ps *Parser) Pending() bool { return len(ps.buf) > 0 }

// Flush returns any buffered bytes as text, and empties the buffer.
func (ps *Parser) Flush() []byte {
	b := append([]byte(nil), ps.buf...)
	ps.buf = ps.buf[:0]
	return b
}

// parseEsc parses one sequence starting at b[0] == ESC.
// It returns the length, the token and whether the sequence was complete.
func parseEsc(b []byte) (int, Token, bool) {
	if len(b) < 2 {
		return 0, Token{}, false
	}
	switch b[1] {
	case '[':
		return parseCSI(b)
	case ']':
		return parseString(b, OSC)
	case 'P':
		return parseString(b, DCS)
	case '_', '^', 'X':
		return parseString(b, StrSeq)
	default:
		// ESC with intermediates (0x20–0x2f) then a final byte, or a simple ESC x.
		j := 1
		for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
			j++
		}
		if j >= len(b) {
			return 0, Token{}, false
		}
		return j + 1, Token{Kind: Esc, Raw: b[:j+1], Final: b[j], Intermediate: b[1:j]}, true
	}
}

func parseCSI(b []byte) (int, Token, bool) {
	j := 2
	t := Token{Kind: CSI}
	if j < len(b) && (b[j] == '?' || b[j] == '>' || b[j] == '<' || b[j] == '=') {
		t.Prefix = b[j]
		j++
	}
	ps := j
	for j < len(b) && b[j] >= 0x30 && b[j] <= 0x3f {
		j++
	}
	t.Params = b[ps:j]
	is := j
	for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
		j++
	}
	t.Intermediate = b[is:j]
	if j >= len(b) {
		return 0, Token{}, false
	}
	t.Final = b[j]
	t.Raw = b[:j+1]
	return j + 1, t, true
}

// parseString parses a sequence that ends with BEL (OSC only) or ST (ESC \).
// An OSC string also ends at any other ESC, as a terminal ends it: the ESC
// then starts the next token, so an OSC token never holds a second sequence.
func parseString(b []byte, kind Kind) (int, Token, bool) {
	for j := 2; j < len(b); j++ {
		switch b[j] {
		case 0x07:
			if kind == OSC {
				return j + 1, Token{Kind: kind, Raw: b[:j+1], Payload: b[2:j]}, true
			}
		case 0x1b:
			if j+1 >= len(b) {
				return 0, Token{}, false
			}
			if b[j+1] == '\\' {
				return j + 2, Token{Kind: kind, Raw: b[:j+2], Payload: b[2:j]}, true
			}
			if kind == OSC {
				return j, Token{Kind: kind, Raw: b[:j], Payload: b[2:j]}, true
			}
		}
	}
	return 0, Token{}, false
}

// Ints parses semicolon-separated decimal parameters. Missing values are -1.
// Colon sub-parameters are cut off (only the main value is kept).
func Ints(params []byte) []int {
	var out []int
	cur, have, sub := 0, false, false
	for _, c := range params {
		switch {
		case c == ';':
			if have {
				out = append(out, cur)
			} else {
				out = append(out, -1)
			}
			cur, have, sub = 0, false, false
		case c == ':':
			sub = true
		case c >= '0' && c <= '9' && !sub:
			cur = cur*10 + int(c-'0')
			have = true
		}
	}
	if have {
		out = append(out, cur)
	} else if len(params) > 0 {
		out = append(out, -1)
	}
	return out
}
