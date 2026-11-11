package client

import (
	"bytes"
	"time"
	"unicode/utf8"
)

// clipReader reads the bytes that go to the terminal, the session's and the
// client's own, for clipboard writes. It only reads them. For each write, it
// gives the hashes of its text to record (clip_text.go), and says whether
// the write went to a selection, such as the primary one, rather than to
// the clipboard.
//
// The guard over-approximates (spec 7.6). It records every reading of the
// two that spec 7.6 names: the pinned Ghostty's, and xterm's with C1
// controls on (clip_c1.go). A reader reads one of them, and the guard
// keeps one of each, which read every byte from the start. It reads:
//
//   - OSC 52 ; selection ; base64. Old kitty joins successive writes, so it
//     also keeps a running text for each selection (clip_osc.go).
//   - kitty's OSC 5522 (clip_kitty.go).
//   - iTerm2's OSC 1337 ; Copy=:base64, and the text between OSC 1337 ;
//     CopyToClipboard and OSC 1337 ; EndCopy.
type clipReader struct {
	state  uint8   // the parser's state (clip_vt.go)
	tbl    vtTable // the reading's state machine
	xterm  bool    // xterm's reading: C1 controls act as their ESC forms
	c1     c1Translator
	record func(sel bool, sums ...[32]byte) error
	// unsure, when set, shares the time until which every drop is refused,
	// when the reader loses track of a write.
	unsure func(until time.Time)
	now    func() time.Time

	osc  oscRead   // the OSC string being read
	term *clipTerm // what the terminal keeps from one attach to the next

	copy    *textHash // iTerm2's copy, while it runs
	copyAt  time.Time
	tail    []byte // the last bytes of ground text, for a character that frames cut
	midRune bool   // the output stopped inside a UTF-8 character
}

// clipTerm is what the terminal itself keeps across a detach: old kitty's
// running OSC 52 text of each selection, kitty's transfer, and when this
// reading lost track of a write. The memory keeps it for the next attach in
// the same process.
type clipTerm struct {
	running map[string]*textHash
	kitty   kittyTransfer
	// orphanAt is when the reader lost track of a write, or when a kitty
	// transfer that it lost track of ended. Every drop is refused for a day
	// after, and while such a transfer runs.
	orphanAt time.Time
}

func newClipTerm() *clipTerm { return &clipTerm{running: map[string]*textHash{}} }

func newClipReader(record func(sel bool, sums ...[32]byte) error, now func() time.Time, term *clipTerm) *clipReader {
	if term == nil {
		term = newClipTerm()
	}
	return &clipReader{tbl: &vtNext, record: record, now: now, term: term}
}

// readAsXterm makes the reader read as xterm with C1 controls on.
func (r *clipReader) readAsXterm() {
	r.xterm, r.tbl = true, &vtNextX
}

// idle reports whether the terminal is between sequences and characters,
// where the client may write bytes of its own without cutting one.
func (r *clipReader) idle() bool {
	return r.state == vtGround && !r.midRune && len(r.c1.pend) == 0
}

// observe reads bytes on their way to the terminal.
func (r *clipReader) observe(b []byte) {
	if r.xterm {
		b = r.c1.translate(b)
	}
	next, next9D := -2, -2 // where the next "]" and 0x9D are: -2 not known, -1 none
	if r.xterm {
		next9D = -1 // xterm's reading has turned every C1 control into ESC forms
	}
	pos := 0
	for pos < len(b) {
		switch r.state {
		case vtOSC:
			pos += r.oscBytes(b[pos:])
		case vtGround:
			if r.copy != nil {
				pos += r.copyText(b[pos:])
				continue
			}
			// Only "]" after ESC, or 0x9D outside ground, starts an OSC
			// string. Agent output has an ESC every few bytes but few of
			// these, so the reader looks for them, and reads the state
			// just before each from the last ESC.
			if next < pos && next != -1 {
				next = indexFrom(b, pos, ']')
			}
			if next9D < pos && next9D != -1 {
				next9D = indexFrom(b, pos, 0x9d)
			}
			i := firstOf(next, next9D)
			if i < 0 {
				r.state = vtEnd(r.tbl, b[pos:])
				pos = len(b)
				break
			}
			s := vtStep(r.tbl, vtEnd(r.tbl, b[pos:i]), b[i])
			pos = i + 1
			r.enter(s)
		default:
			s, n := vtUntil(r.tbl, r.state, b[pos:])
			pos += n
			r.enter(s)
		}
	}
	r.endFrame(b)
}

// endFrame keeps the end of the ground text, so that a character that
// frames cut still counts as cut in the next frame.
func (r *clipReader) endFrame(b []byte) {
	r.midRune = false
	if r.state != vtGround {
		r.tail = r.tail[:0]
		return
	}
	var buf [2 * utf8.UTFMax]byte
	t := append(append(buf[:0], r.tail...), b[max(0, len(b)-utf8.UTFMax):]...)
	t = t[max(0, len(t)-utf8.UTFMax):]
	r.midRune = midRune(t)
	r.tail = append(r.tail[:0], t[max(0, len(t)-(utf8.UTFMax-1)):]...)
}

func indexFrom(b []byte, from int, c byte) int {
	if i := bytes.IndexByte(b[from:], c); i >= 0 {
		return from + i
	}
	return -1
}

func firstOf(a, b int) int {
	switch {
	case a < 0:
		return b
	case b < 0 || a < b:
		return a
	default:
		return b
	}
}

// midRune reports whether b ends inside a UTF-8 character.
func midRune(b []byte) bool {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			return !utf8.FullRune(b[i:])
		}
	}
	return false
}

// enter moves the parser to state s, and starts an OSC string there.
func (r *clipReader) enter(s uint8) {
	if s == vtOSC && r.state != vtOSC {
		r.osc = oscRead{part: oscNum, num: r.osc.num[:0], buf: r.osc.buf[:0], kmeta: r.osc.kmeta}
		r.osc.kmeta.reset()
	}
	r.state = s
}

// copyText reads ground text into iTerm2's copy, up to the next ESC.
func (r *clipReader) copyText(b []byte) int {
	i := bytes.IndexByte(b, 0x1b)
	if i < 0 {
		r.copy.write(b)
		return len(b)
	}
	r.copy.write(b[:i])
	r.state = vtEscape
	return i + 1
}

// oscBytes reads the bytes of an OSC string, and returns how many it read.
func (r *clipReader) oscBytes(b []byte) int {
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x07 || c == 0x18 || c == 0x1a || c == 0x1b:
			// BEL, ST (ESC \) and any ESC end the string. CAN and SUB abort
			// it. The guard counts each as an end, as a terminal that ends
			// it would.
			r.endOSC()
			r.state = r.tbl[vtOSC][c]
			return i + 1
		case c < 0x20:
			i++ // a terminal ignores other controls in the string
		case r.osc.part == osc52Data || r.osc.part == osc5522Data || r.osc.part == osc1337Data:
			n := textRun(b[i:])
			r.oscData(b[i : i+n])
			i += n
		case r.osc.part == oscSkip:
			i += textRun(b[i:])
		default:
			r.oscHead(c)
			i++
		}
	}
	return len(b)
}

// textRun is the length of the text at the start of b, up to a control byte.
func textRun(b []byte) int {
	for i, c := range b {
		if c < 0x20 {
			return i
		}
	}
	return len(b)
}

// rec records hashes. When the journal cannot take them, the reader has
// lost track of a write, and refuses every drop for a day.
func (r *clipReader) rec(sel bool, sums ...[32]byte) {
	if len(sums) > 0 && r.record(sel, sums...) != nil {
		r.lost()
	}
}

// lost notes that the reader lost track of a write. It refuses every drop
// for a day, and shares that with every client process (unsure).
func (r *clipReader) lost() {
	r.term.orphanAt = r.now()
	if r.unsure != nil {
		r.unsure(r.term.orphanAt.Add(clipMaxAge))
	}
}

// startCopy starts iTerm2's copy. A copy that was still running counts as
// a write.
func (r *clipReader) startCopy() {
	r.endCopy()
	r.copy, r.copyAt = newTextHash(), r.now()
}

func (r *clipReader) endCopy() {
	if r.copy == nil {
		return
	}
	if s, ok := r.copy.sum(); ok {
		r.rec(false, s)
	}
	r.copy = nil
}

// detach runs when the attach ends. A kitty transfer that has not ended
// counts as a write, as far as it went: the terminal keeps it, and may set
// the clipboard from it after the next attach.
func (r *clipReader) detach() {
	if s, ok := r.term.kitty.sum(); ok {
		r.rec(r.term.kitty.primary, s)
	}
}

// inProgress is the hashes of the writes that have not ended: iTerm2's
// copy, younger than clipMaxAge, and kitty's transfer.
func (r *clipReader) inProgress() [][32]byte {
	var out [][32]byte
	if r.copy != nil && r.now().Sub(r.copyAt) <= clipMaxAge {
		if s, ok := r.copy.sum(); ok {
			out = append(out, s)
		}
	}
	if s, ok := r.term.kitty.sum(); ok {
		out = append(out, s)
	}
	return out
}

// orphaned reports whether the reader lost track of a write: while a kitty
// transfer that it lost track of runs, and for a day after it, or after
// any other write that it lost. No drop can be checked against such a
// write.
func (r *clipReader) orphaned() bool {
	if k := &r.term.kitty; k.active && k.unsure {
		return true
	}
	t := r.term.orphanAt
	return !t.IsZero() && r.now().Sub(t) <= clipMaxAge
}
