package host

import "strconv"

// sharedModes is the state that a terminal keeps for both screens, and that
// vt reads to place or draw text. vt keeps it for the whole emulator. The
// router follows it, and sets it in each emulator that starts to show,
// unless that emulator has it already. It covers:
//
//   - DEC private modes 6 (origin), 7 (autowrap), 69 (left and right
//     margins) and 2027 (grapheme widths), and ANSI mode 20 (new line);
//   - the G0 to G3 sets that vt knows (A, B and 0), and LS2 and LS3, which
//     put G2 or G3 in use;
//   - the tab stops.
//
// The rest does not carry from one screen to the other. vt keeps the scroll
// margins and the saved cursor for each screen, as terminals do. vt v0.1.0
// does not carry out SO and SI, and it does not read insert mode, or GR for
// UTF-8 text. Colours, the title and the other modes do not change the text.
type sharedModes struct {
	dec     uint8   // the modes of sharedDECModes that are on, one bit each
	newLine bool    // ANSI mode 20
	g       [4]byte // the final bytes that chose G0 to G3, or 0 for ASCII
	gl      byte    // the set in use as GL: 0, or 2 or 3 after LS2 or LS3
	tabs    uint32  // counts changes to the tab stops; 0 while they are vt's default
}

// sharedDECModes are the DEC private modes that carry between the screens.
var sharedDECModes = [...]int{6, 7, 69, 2027}

// freshShared is the shared state of a fresh or reset emulator: autowrap
// on, and the rest off.
var freshShared = sharedModes{dec: 1 << 1}

// followEsc follows an ESC sequence with no intermediate bytes.
func (m *screenModel) followEsc(final byte) {
	switch final {
	case 'H': // HTS
		m.flushTo(m.rt.s)
		tabs := m.tabStops()
		if x := m.current().CursorPosition().X; x >= 0 && x < len(tabs) {
			tabs[x] = true
		}
		m.rt.shared.tabs++
	case 'n': // LS2
		m.rt.shared.gl = 2
	case 'o': // LS3
		m.rt.shared.gl = 3
	}
}

// designate follows ESC ( F, ESC ) F, ESC * F and ESC + F, which choose the
// set F as G0 to G3. vt knows only the sets A, B and 0.
func (m *screenModel) designate(inter, final byte) {
	if inter < '(' || inter > '+' || (final != 'A' && final != 'B' && final != '0') {
		return
	}
	if final == 'B' {
		final = 0
	}
	m.rt.shared.g[inter-'('] = final
}

// followDECModes follows the shared modes in DEC private mode parameters.
func (m *screenModel) followDECModes(params []byte, on bool) {
	for len(params) > 0 {
		var n int
		n, params = nextMode(params)
		m.followDECMode(n, on)
	}
}

// followDECMode follows one DEC private mode.
func (m *screenModel) followDECMode(n int, on bool) {
	for k, mode := range sharedDECModes {
		if n != mode {
			continue
		}
		if on {
			m.rt.shared.dec |= 1 << k
		} else {
			m.rt.shared.dec &^= 1 << k
		}
	}
}

// followANSIModes follows the shared modes in ANSI mode parameters.
func (m *screenModel) followANSIModes(params []byte, on bool) {
	for len(params) > 0 {
		var n int
		n, params = nextMode(params)
		if n == 20 {
			m.rt.shared.newLine = on
		}
	}
}

// clearTabs follows TBC: 0 clears the tab stop at the cursor, and 3 clears
// them all.
func (m *screenModel) clearTabs(params []byte) {
	n, _ := nextMode(params)
	switch {
	case n <= 0:
		tabs := m.tabStops()
		if x := m.current().CursorPosition().X; x >= 0 && x < len(tabs) {
			tabs[x] = false
		}
	case n == 3:
		clear(m.tabStops())
	default:
		return
	}
	m.rt.shared.tabs++
}

// tabStops returns the tab stops that the router follows. vt's default is a
// stop every 8 columns, from the first.
func (m *screenModel) tabStops() []bool {
	if m.rt.tabs == nil {
		m.rt.tabs = make([]bool, m.e.Width())
		for x := 0; x < len(m.rt.tabs); x += 8 {
			m.rt.tabs[x] = true
		}
	}
	return m.rt.tabs
}

// resetTabs follows a reset or a resize, after which vt has its default tab
// stops in every emulator.
func (m *screenModel) resetTabs() {
	m.rt.tabs = nil
	m.rt.shared.tabs, m.rt.mainShared.tabs = 0, 0
}

// passSharedModes sets the shared state in the emulator of the screen that
// shows.
func (m *screenModel) passSharedModes() {
	s := m.rt.shared
	b := make([]byte, 0, 64)
	for k, n := range sharedDECModes {
		b = append(strconv.AppendInt(append(b, "\x1b[?"...), int64(n), 10), setOrReset(s.dec&(1<<k) != 0))
	}
	b = append(b, "\x1b[20"...)
	b = append(b, setOrReset(s.newLine))
	for k, g := range s.g {
		if g == 0 {
			g = 'B'
		}
		b = append(b, 0x1b, "()*+"[k], g)
	}
	// GL can only move on from G0 in vt, and only RIS brings it back.
	switch s.gl {
	case 2:
		b = append(b, "\x1bn"...)
	case 3:
		b = append(b, "\x1bo"...)
	}
	if s.tabs != 0 {
		// Clear the stops, set each one with the cursor, then put the
		// cursor back in its column.
		b = append(b, "\x1b[3g"...)
		for x, stop := range m.tabStops() {
			if stop {
				b = append(strconv.AppendInt(append(b, "\x1b["...), int64(x+1), 10), "G\x1bH"...)
			}
		}
		x := m.current().CursorPosition().X
		b = append(strconv.AppendInt(append(b, "\x1b["...), int64(x+1), 10), 'G')
	}
	m.pass(b)
}

// setOrReset returns the final byte that sets a mode (h), or resets it (l).
func setOrReset(on bool) byte {
	if on {
		return 'h'
	}
	return 'l'
}

// nextMode reads the first mode of mode parameters, or -1 for none. It also
// returns the parameters after the first separator. vt reads a colon as it
// reads a semicolon here, so the router does too.
func nextMode(params []byte) (n int, rest []byte) {
	n = -1
	for i, c := range params {
		switch {
		case c == ';' || c == ':':
			return n, params[i+1:]
		case c >= '0' && c <= '9' && n < 1<<20:
			n = max(n, 0)*10 + int(c-'0')
		}
	}
	return n, nil
}

// switchesScreen reports whether DEC private mode parameters hold 47, 1047
// or 1049, the modes that switch between the main and alternate screens.
func switchesScreen(params []byte) bool {
	for len(params) > 0 {
		var n int
		n, params = nextMode(params)
		if n == 47 || n == 1047 || n == 1049 {
			return true
		}
	}
	return false
}

// privateModes follows a DEC private mode sequence that switches screens.
// Its modes take effect in turn, as in a terminal. So the modes before a
// switch go to one emulator, and the modes after it to the other.
func (m *screenModel) privateModes(params []byte, on bool) {
	for len(params) > 0 {
		var n int
		n, params = nextMode(params)
		switch n {
		case -1:
			// An empty mode changes nothing.
		case 1049:
			if on {
				// 1049 saves the cursor first, as DECSC does. For a switch from
				// the main screen, that save is all the main emulator sees.
				m.pass(seqDECSC)
			}
			m.showAlt(on, n)
		case 1047, 47:
			m.showAlt(on, n)
		default:
			m.followDECMode(n, on)
			m.pass(append(strconv.AppendInt([]byte("\x1b[?"), int64(n), 10), setOrReset(on)))
		}
	}
}
