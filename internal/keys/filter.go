// Package keys finds the client's own keys in the user's input, in every way a
// terminal can encode them, and passes every other byte through unchanged.
//
// While attached, the client owns only two keys: detach (default Ctrl+\) and
// image paste (default Ctrl+V). A terminal may send them as legacy control bytes,
// as kitty keyboard protocol CSI u sequences (with event types, lock bits and
// alternate keys), or as xterm modifyOtherKeys sequences. The filter collects a
// bracketed paste whole, so the client can rewrite dropped file paths, and it
// never scans a paste for keys. If a read splits a paste's start marker right
// after its ESC, that ESC has already gone out. The paste then passes through
// as it came, and the client cannot rewrite its paths.
package keys

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Modifier bits, as in the kitty keyboard protocol (value - 1).
const (
	ModShift = 1
	ModAlt   = 2
	ModCtrl  = 4
	ModSuper = 8
	lockBits = 64 | 128 // caps lock and num lock are ignored
)

// Key is one key with modifiers.
type Key struct {
	Code rune // the unshifted key, for example '\\' or 'v'
	Mods int  // ModCtrl and friends
}

// String formats a key as "ctrl+\".
func (k Key) String() string {
	var parts []string
	if k.Mods&ModCtrl != 0 {
		parts = append(parts, "ctrl")
	}
	if k.Mods&ModAlt != 0 {
		parts = append(parts, "alt")
	}
	if k.Mods&ModShift != 0 {
		parts = append(parts, "shift")
	}
	if k.Mods&ModSuper != 0 {
		parts = append(parts, "super")
	}
	return strings.Join(append(parts, string(k.Code)), "+")
}

// Parse reads a key such as "ctrl+\" or "alt+v".
func Parse(s string) (Key, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Key{}, errors.New("empty key")
	}
	var k Key
	parts := strings.Split(s, "+")
	// "ctrl++" means ctrl and the plus key.
	if strings.HasSuffix(s, "++") {
		parts = append(strings.Split(strings.TrimSuffix(s, "++"), "+"), "+")
	}
	last := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl", "control":
			k.Mods |= ModCtrl
		case "alt", "opt", "option", "meta":
			k.Mods |= ModAlt
		case "shift":
			k.Mods |= ModShift
		case "super", "cmd":
			k.Mods |= ModSuper
		default:
			return Key{}, fmt.Errorf("unknown modifier %q in %q", p, s)
		}
	}
	r := []rune(last)
	if len(r) != 1 {
		return Key{}, fmt.Errorf("the key in %q must be one character", s)
	}
	k.Code = r[0]
	return k, nil
}

// legacy returns the bytes a terminal sends for k without any keyboard protocol,
// or nil when there is no unique legacy encoding.
func (k Key) legacy() []byte {
	c := k.Code
	switch k.Mods {
	case ModCtrl:
		switch {
		case c >= 'a' && c <= 'z':
			return []byte{byte(c - 'a' + 1)}
		case c >= '@' && c <= '_': // @ [ \ ] ^ _
			return []byte{byte(c - '@')}
		case c == '?':
			return []byte{0x7f}
		}
	case ModAlt:
		if c < 0x80 {
			return []byte{0x1b, byte(c)} //nolint:gosec // G115: c is below 0x80, so it fits in a byte
		}
	case 0:
		return []byte(string(c))
	}
	return nil
}

// Kind of a piece of filtered input.
type Kind int

// Piece kinds.
const (
	Pass  Kind = iota // bytes to forward unchanged
	Match             // one of the watched keys
	Paste             // a complete bracketed paste
)

// Piece is one part of the filtered input.
type Piece struct {
	Kind Kind
	// Raw is the exact input bytes of this piece.
	Raw []byte
	// For Match: which watched key (its index), and whether this is a release or repeat event.
	Index   int
	Release bool
	Repeat  bool
	// For Paste: the pasted text, without the bracketing markers.
	Text []byte
}

// Filter splits input into pass-through bytes, watched keys and pastes.
type Filter struct {
	keys    []Key
	legacy  [][]byte
	buf     []byte
	inPaste bool
	paste   bytes.Buffer
	// sent counts the bytes of a paste's start marker that went out before
	// the filter could tell. They are a lone ESC at the end of a read, and
	// the bytes right after it. In a paste, its piece leaves them out.
	sent int
}

// MaxPaste is the largest paste collected whole; larger pastes pass through as they come.
const MaxPaste = 1 << 20

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// New returns a filter that watches for the given keys.
func New(watch ...Key) *Filter {
	f := &Filter{keys: watch}
	for _, k := range watch {
		f.legacy = append(f.legacy, k.legacy())
	}
	return f
}

// Feed filters p. The bytes of an unfinished CSI sequence are kept until the
// next call, or until Flush. A lone ESC at the end of p goes out at once. Use
// Pending to know whether to arm a short flush timer.
func (f *Filter) Feed(p []byte) []Piece {
	f.buf = append(f.buf, p...)
	s := &scan{b: f.buf}
	i, stop := f.resume(s), false
	for i < len(s.b) && !stop {
		switch {
		case f.inPaste:
			i, stop = f.feedPaste(s, i)
		case s.b[i] != 0x1b:
			i = f.feedByte(s, i)
		default:
			i, stop = f.feedEsc(s, i)
		}
	}
	if !f.inPaste {
		s.pass(i)
	}
	// In a paste, the bytes up to i went into the paste buffer.
	rest := append([]byte(nil), s.b[i:]...)
	f.buf = append(f.buf[:0], rest...)
	return dropEmpty(s.out)
}

// scan is one Feed's progress: the bytes, the pieces so far, and where the
// bytes that pass through unchanged begin.
type scan struct {
	b         []byte
	out       []Piece
	passStart int
}

// pass emits the bytes from passStart up to end as one pass-through piece.
func (s *scan) pass(end int) {
	if end > s.passStart {
		s.out = append(s.out, Piece{Kind: Pass, Raw: append([]byte(nil), s.b[s.passStart:end]...)})
	}
}

// match emits what passed before i, then the n bytes at i as piece p, and
// returns the index after them.
func (s *scan) match(i, n int, p Piece) int {
	s.pass(i)
	p.Kind, p.Raw = Match, append([]byte(nil), s.b[i:i+n]...)
	s.out = append(s.out, p)
	s.passStart = i + n
	return i + n
}

// resume goes on from the last read when it ended in a lone ESC. That ESC
// went out at once, perhaps with more of a paste's start marker after it.
// When the new bytes finish the marker, the filter still collects the paste,
// and never scans its text for keys. It returns where the scan starts.
func (f *Filter) resume(s *scan) int {
	if f.inPaste || f.sent == 0 {
		return 0
	}
	sent := f.sent
	f.sent = 0
	rest := pasteStart[sent:]
	n := min(len(rest), len(s.b))
	if !bytes.Equal(s.b[:n], rest[:n]) {
		return 0 // not a paste: scan the bytes as usual
	}
	if n < len(rest) {
		// Maybe a paste still: these bytes go out at once too.
		f.sent = sent + n
		return n
	}
	f.inPaste, f.sent = true, sent
	s.passStart = n
	return n
}

// feedPaste collects a bracketed paste from i. stop is true when the input
// ran out before the paste's end marker.
func (f *Filter) feedPaste(s *scan, i int) (next int, stop bool) {
	b := s.b
	j := bytes.Index(b[i:], pasteEnd)
	if j < 0 {
		// Keep all but a possible partial end marker.
		keep := min(len(b)-i, len(pasteEnd)-1)
		f.paste.Write(b[i : len(b)-keep])
		i = len(b) - keep
		if f.paste.Len() > MaxPaste {
			s.out = append(s.out, Piece{Kind: Pass, Raw: append(append([]byte(nil), pasteStart[f.sent:]...), f.paste.Bytes()...)})
			f.paste.Reset()
			f.inPaste, f.sent = false, 0
			s.passStart = i
			// The rest of this paste, and its end marker, pass through as normal input.
		}
		return i, true
	}
	f.paste.Write(b[i : i+j])
	text := append([]byte(nil), f.paste.Bytes()...)
	raw := append(append(append([]byte(nil), pasteStart[f.sent:]...), text...), pasteEnd...)
	if f.sent > 0 {
		// The start of its marker went out already, so the paste cannot be
		// rewritten: the rest of it passes through as it came.
		s.out = append(s.out, Piece{Kind: Pass, Raw: raw})
	} else {
		s.out = append(s.out, Piece{Kind: Paste, Raw: raw, Text: text})
	}
	f.paste.Reset()
	f.inPaste, f.sent = false, 0
	i += j + len(pasteEnd)
	s.passStart = i
	return i, false
}

// feedByte handles a byte that does not start an escape sequence.
func (f *Filter) feedByte(s *scan, i int) int {
	if idx := f.matchLegacyByte(s.b[i]); idx >= 0 {
		return s.match(i, 1, Piece{Index: idx})
	}
	return i + 1
}

// feedEsc handles ESC at i: a sequence, an Alt+key, the start of a paste,
// or the Esc key itself. stop is true when the sequence is not complete yet.
func (f *Filter) feedEsc(s *scan, i int) (next int, stop bool) {
	b := s.b
	if i+1 >= len(b) {
		// A lone ESC at the end of a read is almost always the Esc key, so it
		// goes out at once. A terminal writes each key in one write, so a read
		// rarely ends right after the ESC of a longer key. When it does, the
		// rest of that key comes as plain bytes and passes through unchanged.
		// A split then costs a missed key, never a changed byte:
		//   - Legacy Ctrl+\ (0x1c) and Ctrl+V (0x16) have no ESC, so a split
		//     never hides them.
		//   - Legacy Alt+V (ESC v), the image paste key on Windows, goes to
		//     the agent as those two bytes.
		//   - A kitty CSI u or a modifyOtherKeys key goes to the agent as its
		//     own bytes: a press, a repeat or a release. After a press that
		//     the client took, the agent can get a release without its press.
		//   - A double tap of the detach key sends Ctrl+\ to the agent. If a
		//     read splits the second press in the kitty or modifyOtherKeys
		//     form, the agent gets Ctrl+\, and the client still detaches.
		//   - A paste's start marker is still found (see resume), but the
		//     paste then passes through as it came: no dropped file is
		//     uploaded.
		// A key split later, inside its CSI, is held until it is complete, and
		// still found.
		f.sent = 1
		return i + 1, false
	}
	if bytes.HasPrefix(b[i:], pasteStart) {
		s.pass(i)
		f.inPaste = true
		s.passStart = i + len(pasteStart)
		return s.passStart, false
	}
	if b[i+1] == '[' {
		n, complete, valid := csiLen(b[i:])
		switch {
		case !complete:
			return i, true
		case !valid:
			return i + n, false // not a real CSI: pass it through untouched
		}
		if idx, rel, rep, ok := f.matchCSI(b[i : i+n]); ok {
			return s.match(i, n, Piece{Index: idx, Release: rel, Repeat: rep}), false
		}
		return i + n, false
	}
	// Legacy Alt+key: ESC followed by the key.
	if idx := f.matchLegacyAlt(b[i : i+2]); idx >= 0 {
		return s.match(i, 2, Piece{Index: idx}), false
	}
	return i + 1, false
}

func dropEmpty(ps []Piece) []Piece {
	out := ps[:0]
	for _, p := range ps {
		if p.Kind == Pass && len(p.Raw) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Pending reports whether the filter waits for more input. Either it holds
// back an unfinished CSI, or a lone ESC went out, and the rest of a paste's
// start marker may follow it. Arm a short timer, and call Flush when it fires.
func (f *Filter) Pending() bool { return !f.inPaste && (len(f.buf) > 0 || f.sent > 0) }

// Flush returns held-back bytes as pass-through input, and ends the wait that
// Pending reports.
func (f *Filter) Flush() []byte {
	if f.inPaste {
		return nil
	}
	f.sent = 0
	b := append([]byte(nil), f.buf...)
	f.buf = f.buf[:0]
	return b
}

func (f *Filter) matchLegacyByte(c byte) int {
	for i, l := range f.legacy {
		if len(l) == 1 && l[0] == c && c < 0x20 {
			return i
		}
	}
	return -1
}

func (f *Filter) matchLegacyAlt(two []byte) int {
	for i, l := range f.legacy {
		if len(l) == 2 && bytes.Equal(l, two) {
			return i
		}
	}
	return -1
}

// csiLen measures the CSI sequence at b. complete is false when more bytes are
// needed. valid is false when the bytes are not a well-formed CSI; then n bytes
// should pass through untouched.
func csiLen(b []byte) (n int, complete, valid bool) {
	for j := 2; j < len(b); j++ {
		c := b[j]
		if c >= 0x40 && c <= 0x7e {
			return j + 1, true, true
		}
		if c < 0x20 || c > 0x7e {
			return j, true, false
		}
	}
	return 0, false, false
}

// matchCSI matches kitty CSI u and xterm modifyOtherKeys encodings of a watched key.
func (f *Filter) matchCSI(seq []byte) (idx int, release, repeat, ok bool) {
	if len(seq) < 3 {
		return
	}
	body := seq[2 : len(seq)-1]
	if len(body) > 0 && (body[0] < '0' || body[0] > '9') {
		return 0, false, false, false // has a prefix such as ? > < =: not a key
	}
	code, mods, event, ok := parseKeyCSI(seq[len(seq)-1], strings.Split(string(body), ";"))
	if !ok {
		return 0, false, false, false
	}
	m := (mods - 1) &^ lockBits
	for i, k := range f.keys {
		if int(k.Code) == code && m == k.Mods {
			return i, event == 3, event == 2, true
		}
	}
	return 0, false, false, false
}

// parseKeyCSI reads a key from the fields of a CSI sequence that ends in
// final: the kitty form, CSI code[:shifted[:base]] ; mods[:event] [; text] u,
// or xterm's modifyOtherKeys form, CSI 27 ; mods ; code ~.
func parseKeyCSI(final byte, fields []string) (code, mods, event int, ok bool) {
	var err error
	switch final {
	case 'u':
		c, _, _ := strings.Cut(fields[0], ":")
		if code, err = strconv.Atoi(c); err != nil {
			return 0, 0, 0, false
		}
		mods, event = 1, 1
		if len(fields) > 1 && fields[1] != "" {
			ms, ev, hasEvent := strings.Cut(fields[1], ":")
			if mods, err = strconv.Atoi(ms); err != nil {
				return 0, 0, 0, false
			}
			if hasEvent {
				ev, _, _ = strings.Cut(ev, ":")
				if event, err = strconv.Atoi(ev); err != nil {
					return 0, 0, 0, false
				}
			}
		}
		return code, mods, event, true
	case '~':
		if len(fields) != 3 || fields[0] != "27" {
			return 0, 0, 0, false
		}
		if mods, err = strconv.Atoi(fields[1]); err != nil {
			return 0, 0, 0, false
		}
		if code, err = strconv.Atoi(fields[2]); err != nil {
			return 0, 0, 0, false
		}
		return code, mods, 1, true
	default:
		return 0, 0, 0, false
	}
}
