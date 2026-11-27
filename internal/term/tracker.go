package term

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
)

// Tracker watches a session's output and remembers the terminal modes the
// session changed, so the client can undo them on detach and set them again
// on reattach. It only reads the stream.
type Tracker struct {
	p Parser
	M Modes
}

// Modes is what a session has changed in the terminal.
type Modes struct {
	AltScreen bool
	// DEC private modes the session turned on (true) or off (false), by number.
	Private map[int]bool
	// Kitty keyboard flag stacks, for the main and the alternate screen.
	KittyMain []int
	KittyAlt  []int
	// KittySet is true when the session used CSI = … u outside any push.
	KittySet bool
	// ModifyOtherKeys is the xterm modifyOtherKeys level, or -1.
	ModifyOtherKeys int
	// CursorShape is the DECSCUSR value, or -1.
	CursorShape int
	// Keypad is true after DECKPAM (ESC =).
	Keypad bool
	// Insert is ANSI mode 4.
	Insert bool
	// ScrollTop and ScrollBottom are the DECSTBM margins (1-based), or 0.
	ScrollTop, ScrollBottom int
	// OSC sequences that set colours (10, 11, 12, 4, 17, 19), by key, to replay on restore.
	Colors map[string][]byte
	// Title is the last OSC 0 or 2 sequence.
	Title []byte
	// Pointer is true when the session changed the pointer shape (OSC 22).
	Pointer bool
}

// tracked lists the DEC private modes the tracker handles.
var tracked = map[int]bool{
	1: true, 6: true, 7: true, 9: true, 12: true, 25: true,
	1000: true, 1001: true, 1002: true, 1003: true, 1004: true, 1005: true, 1006: true,
	1007: true, 1015: true, 1016: true, 2004: true, 2026: true, 2031: true,
}

// NewTracker returns a tracker with default modes.
func NewTracker() *Tracker {
	t := &Tracker{}
	t.Reset()
	return t
}

// Reset forgets everything, as after RIS.
func (t *Tracker) Reset() {
	t.M = Modes{Private: map[int]bool{}, ModifyOtherKeys: -1, CursorShape: -1, Colors: map[string][]byte{}}
}

// Feed reads more output.
func (t *Tracker) Feed(p []byte) {
	t.p.Feed(p, t.token)
}

func (t *Tracker) kitty() *[]int {
	if t.M.AltScreen {
		return &t.M.KittyAlt
	}
	return &t.M.KittyMain
}

func (t *Tracker) token(tok Token) {
	switch tok.Kind {
	case CSI:
		t.csiToken(tok)
	case Esc:
		t.escToken(tok)
	case OSC:
		t.oscToken(tok)
	default:
		// Text, DCS and other strings change no mode the tracker keeps.
	}
}

// csiToken handles the control sequences that set modes.
func (t *Tracker) csiToken(tok Token) {
	m := &t.M
	plain := len(tok.Intermediate) == 0
	switch {
	case tok.Prefix == '?' && (tok.Final == 'h' || tok.Final == 'l') && plain:
		t.privateModes(Ints(tok.Params), tok.Final == 'h')
	case tok.Prefix == 0 && (tok.Final == 'h' || tok.Final == 'l') && plain:
		for _, n := range Ints(tok.Params) {
			if n == 4 {
				m.Insert = tok.Final == 'h'
			}
		}
	case tok.Final == 'u' && plain:
		t.kittyKeys(tok.Prefix, Ints(tok.Params))
	case tok.Prefix == '>' && (tok.Final == 'm' || tok.Final == 'n'):
		t.modifyOtherKeys(tok.Final, Ints(tok.Params))
	case tok.Prefix == 0 && tok.Final == 'q' && bytes.Equal(tok.Intermediate, []byte(" ")):
		m.CursorShape = -1
		if v := Ints(tok.Params); len(v) > 0 && v[0] > 0 {
			m.CursorShape = v[0]
		}
	case tok.Prefix == 0 && tok.Final == 'r' && plain:
		v := Ints(tok.Params)
		m.ScrollTop, m.ScrollBottom = 0, 0
		if len(v) >= 2 && v[0] > 0 && v[1] > 0 {
			m.ScrollTop, m.ScrollBottom = v[0], v[1]
		}
	case tok.Prefix == 0 && tok.Final == 'p' && bytes.Equal(tok.Intermediate, []byte("!")):
		t.softReset()
	}
}

// modifyOtherKeys follows xterm's modifyOtherKeys level: CSI > 4 ; n m sets
// it, and CSI > 4 m or CSI > 4 n resets it.
func (t *Tracker) modifyOtherKeys(final byte, v []int) {
	if len(v) == 0 || v[0] != 4 {
		return
	}
	t.M.ModifyOtherKeys = -1
	if final == 'm' && len(v) >= 2 && v[1] > 0 {
		t.M.ModifyOtherKeys = v[1]
	}
}

// privateModes records DEC private modes set (on) or reset.
func (t *Tracker) privateModes(modes []int, on bool) {
	m := &t.M
	for _, n := range modes {
		switch n {
		case 1049, 1047, 47:
			if on && !m.AltScreen {
				m.KittyAlt = nil
			}
			m.AltScreen = on
		default:
			if tracked[n] {
				m.Private[n] = on
			}
		}
	}
}

// kittyKeys follows the kitty keyboard flag stack of the current screen:
// CSI > f u pushes, CSI < n u pops, CSI = f u sets the top.
func (t *Tracker) kittyKeys(prefix byte, v []int) {
	st := t.kitty()
	first := func(def int) int {
		if len(v) > 0 && v[0] > 0 {
			return v[0]
		}
		return def
	}
	switch prefix {
	case '>':
		*st = append(*st, first(0))
		if len(*st) > 64 {
			*st = (*st)[1:]
		}
	case '<':
		if n := first(1); n >= len(*st) {
			*st = (*st)[:0]
		} else {
			*st = (*st)[:len(*st)-n]
		}
	case '=':
		if f := first(0); len(*st) > 0 {
			(*st)[len(*st)-1] = f
		} else {
			t.M.KittySet = f != 0
		}
	}
}

// softReset is DECSTR: modes go back to their defaults, but the screen, the
// keyboard stacks, the colours and the title stay.
func (t *Tracker) softReset() {
	m := &t.M
	alt := m.AltScreen
	main, altStack := m.KittyMain, m.KittyAlt
	colors, title := m.Colors, m.Title
	t.Reset()
	m.AltScreen, m.KittyMain, m.KittyAlt, m.Colors, m.Title = alt, main, altStack, colors, title
}

// escToken handles the keypad modes and RIS.
func (t *Tracker) escToken(tok Token) {
	if len(tok.Intermediate) != 0 {
		return
	}
	switch tok.Final {
	case '=':
		t.M.Keypad = true
	case '>':
		t.M.Keypad = false
	case 'c':
		t.Reset()
	}
}

// oscToken keeps the title, the colours and the pointer shape.
func (t *Tracker) oscToken(tok Token) {
	m := &t.M
	num, rest, _ := bytes.Cut(tok.Payload, []byte(";"))
	switch string(num) {
	case "0", "2":
		m.Title = ended(tok.Raw)
	case "10", "11", "12", "17", "19":
		if !bytes.HasSuffix(rest, []byte("?")) {
			m.Colors[string(num)] = ended(tok.Raw)
		}
	case "4":
		if !bytes.Contains(rest, []byte("?")) {
			idx, _, _ := bytes.Cut(rest, []byte(";"))
			m.Colors["4;"+string(idx)] = ended(tok.Raw)
		}
	case "104":
		for k := range m.Colors {
			if len(k) > 2 && k[:2] == "4;" {
				delete(m.Colors, k)
			}
		}
	case "110", "111", "112", "117", "119":
		delete(m.Colors, string(num[1:]))
	case "22":
		m.Pointer = len(rest) > 0
	}
}

// ended is a copy of an OSC sequence to send again, with an ST when an ESC
// that started the next sequence ended it.
func ended(raw []byte) []byte {
	out := append([]byte(nil), raw...)
	if !bytes.HasSuffix(out, []byte("\a")) && !bytes.HasSuffix(out, []byte("\x1b\\")) {
		out = append(out, "\x1b\\"...)
	}
	return out
}

func csi(b *bytes.Buffer, s string) { b.WriteString("\x1b[" + s) }

// ResetSequence returns the sequence that undoes every change the session made,
// in a safe order. It never contains RIS.
func (m *Modes) ResetSequence() []byte {
	var b bytes.Buffer
	csi(&b, "?2026l") // end any synchronized update first
	if m.AltScreen {
		if n := len(m.KittyAlt); n > 0 {
			csi(&b, fmt.Sprintf("<%du", n))
		}
		csi(&b, "?1049l")
	}
	if n := len(m.KittyMain); n > 0 {
		csi(&b, fmt.Sprintf("<%du", n))
	}
	if m.KittySet {
		csi(&b, "=0;1u")
	}
	// Mouse, focus and paste modes: turn all of them off, which is always safe.
	for _, n := range []int{1000, 1001, 1002, 1003, 1005, 1006, 1015, 1016, 9, 1004, 2004} {
		csi(&b, fmt.Sprintf("?%dl", n))
	}
	if on, ok := m.Private[1007]; ok && on {
		csi(&b, "?1007l")
	}
	if on, ok := m.Private[2031]; ok && on {
		csi(&b, "?2031l")
	}
	if on, ok := m.Private[1]; ok && on {
		csi(&b, "?1l")
	}
	if m.Keypad {
		b.WriteString("\x1b>")
	}
	if on, ok := m.Private[6]; ok && on {
		csi(&b, "?6l")
	}
	if on, ok := m.Private[7]; ok && !on {
		csi(&b, "?7h")
	}
	if m.Insert {
		csi(&b, "4l")
	}
	if m.ScrollTop > 0 {
		b.WriteString("\x1b7")
		csi(&b, "r")
		b.WriteString("\x1b8")
	}
	if m.ModifyOtherKeys >= 0 {
		csi(&b, ">4m")
	}
	if m.CursorShape >= 0 {
		csi(&b, "0 q")
	}
	csi(&b, "?25h")
	if len(m.Colors) > 0 {
		b.WriteString("\x1b]104\x1b\\\x1b]110\x1b\\\x1b]111\x1b\\\x1b]112\x1b\\")
	}
	if m.Pointer {
		b.WriteString("\x1b]22;\x1b\\")
	}
	csi(&b, "0m")
	return b.Bytes()
}

// RestoreSequence returns the sequence that puts the session's modes back,
// for a reattach that continues from the bytes the terminal already shows.
func (m *Modes) RestoreSequence() []byte {
	var b bytes.Buffer
	for _, f := range m.KittyMain {
		csi(&b, ">"+strconv.Itoa(f)+"u")
	}
	if m.KittySet && len(m.KittyMain) == 0 {
		csi(&b, "=1;1u")
	}
	if m.AltScreen {
		csi(&b, "?1049h")
		for _, f := range m.KittyAlt {
			csi(&b, ">"+strconv.Itoa(f)+"u")
		}
	}
	keys := make([]int, 0, len(m.Private))
	for k := range m.Private {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		if k == 2026 {
			continue
		}
		if m.Private[k] {
			csi(&b, fmt.Sprintf("?%dh", k))
		} else {
			csi(&b, fmt.Sprintf("?%dl", k))
		}
	}
	if m.Keypad {
		b.WriteString("\x1b=")
	}
	if m.Insert {
		csi(&b, "4h")
	}
	if m.ScrollTop > 0 {
		b.WriteString("\x1b7")
		csi(&b, fmt.Sprintf("%d;%dr", m.ScrollTop, m.ScrollBottom))
		b.WriteString("\x1b8")
	}
	if m.ModifyOtherKeys >= 0 {
		csi(&b, fmt.Sprintf(">4;%dm", m.ModifyOtherKeys))
	}
	if m.CursorShape >= 0 {
		csi(&b, fmt.Sprintf("%d q", m.CursorShape))
	}
	keysC := make([]string, 0, len(m.Colors))
	for k := range m.Colors {
		keysC = append(keysC, k)
	}
	sort.Strings(keysC)
	for _, k := range keysC {
		b.Write(m.Colors[k])
	}
	if len(m.Title) > 0 {
		b.Write(m.Title)
	}
	return b.Bytes()
}
