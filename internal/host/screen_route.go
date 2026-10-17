package host

// The router splits the program's output between the screen model's two
// emulators. It reads escape sequences as vt's parser does, so that both
// agree on where each one starts and ends. It drops two kinds of bytes:
//
//   - Control strings: OSC, DCS, APC, PM and SOS. They draw no text, and the
//     scanner reads the title. vt ends one at byte 0x9C, which is also part
//     of UTF-8 text, such as "✳" in a title. The router ends one as a
//     terminal does: at BEL (an OSC only), ST, CAN or SUB, or at any other
//     ESC, which starts the next sequence. It keeps none of a string's bytes.
//   - A C1 control (0x80 to 0x9F) outside a UTF-8 character. vt acts on one,
//     and 0x9B starts a CSI, but a terminal that reads UTF-8 does not.
//
// A sequence waits in the router until it knows where the sequence goes, also
// when a read cuts it. A sequence that goes on unchanged stays in place, so a
// read with no switch and no string reaches the emulator in one write. A C0
// control right after ESC goes on at once, as vt carries it out at once. A C0
// control inside a CSI garbles it in vt (seqControl), so that CSI goes on as it
// comes. vt reads the bytes after a UTF-8 lead byte as part of the character,
// whatever they are, even ESC, and so does the router (screen_text.go).

// routeState is where the router is in the output.
type routeState uint8

const (
	routeText      routeState = iota // text and controls
	routeEsc                         // after ESC
	routeEscSeq                      // an ESC sequence with intermediate bytes
	routeCSIEntry                    // after ESC [
	routeCSI                         // the parameters of a CSI
	routePass                        // a sequence passed on as it comes
	routeString                      // a control string, dropped
	routeStringEsc                   // ESC inside a control string
)

// screenMaxHeld is the most that a sequence can hold while it waits. A longer
// one is not a sequence that the router follows, so it goes on as it comes.
const screenMaxHeld = 256

// router is the router's state between reads, and within one.
type router struct {
	state    routeState
	runeLeft int    // the bytes still to come of a UTF-8 character
	held     []byte // the start of a sequence that an earlier read cut
	// Within a read: p is the read, p[r:] is neither passed on nor dropped
	// yet, and the sequence that waits starts at p[s].
	p        []byte
	r, s     int
	inter    byte // the first intermediate byte of an ESC sequence
	inters   int  // how many intermediate bytes it has
	prefix   byte // the CSI's private marker, such as '?', or 0
	passFrom byte // the lowest final byte of the sequence that goes on
	osc      bool // the control string is an OSC, which BEL also ends
	// The state that both screens share, as the main emulator has it and
	// as the router follows it (screen_shared.go).
	shared, mainShared sharedModes
	tabs               []bool // the tab stops, by column, or nil for vt's default
}

// route passes the output to the emulator of the screen that shows.
func (m *screenModel) route(p []byte) {
	rt := &m.rt
	rt.p, rt.r, rt.s = p, 0, 0
	for i := 0; i < len(p); i++ {
		switch rt.state {
		case routeText:
			i = m.text(i)
		case routeEsc:
			i = m.escByte(i)
		case routeEscSeq:
			i = m.escSeqByte(i)
		case routeCSIEntry:
			i = m.csiEntryByte(i)
		case routeCSI:
			i = m.csiByte(i)
		case routePass:
			i = m.passByte(i)
		case routeString:
			i = m.stringBytes(i)
		case routeStringEsc:
			i = m.stringEscByte(i)
		}
	}
	switch rt.state {
	case routeEsc, routeEscSeq, routeCSIEntry, routeCSI:
		m.flushTo(rt.s)
		rt.held = append(rt.held, p[rt.s:]...)
	case routeString, routeStringEsc:
	default:
		m.flushTo(len(p))
	}
	rt.p = nil
}

// Each byte reader below reads p[i] and maybe more, and returns the index of
// the last byte that it read. It returns i-1 for a byte that the next state
// reads again.

// escByte reads the byte after ESC. vt carries out a C0 control here at
// once, and a second ESC replaces the first. Either way, the sequence that
// follows is whole, as vt starts it afresh when it leaves this state.
func (m *screenModel) escByte(i int) int {
	rt := &m.rt
	switch b := rt.p[i]; {
	case b == '[':
		if end := m.wholeCSI(i + 1); end > 0 {
			return m.csiEnd(end)
		}
		rt.state = routeCSIEntry
	case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
		m.dropPending(i + 1)
		rt.state, rt.osc = routeString, b == ']'
	case b == 'c':
		m.dropPending(i + 1)
		rt.state = routeText
		m.reset()
	case b >= 0x20 && b < 0x30:
		rt.state, rt.inter, rt.inters = routeEscSeq, b, 1
	case b >= 0x30 && b < 0x7f:
		m.followEsc(b)
		m.keepPending(i)
	case b == 0x7f:
		// vt skips DEL here.
	case b == 0x1b:
		m.dropPending(i)
	case b < 0x20 && b != 0x18 && b != 0x1a:
		// Carry out the C0 control now, and keep the ESC waiting.
		m.flushTo(rt.s)
		rt.held = append(rt.held, rt.p[rt.s:i]...)
		m.pass(rt.p[i : i+1])
		rt.r, rt.s = i+1, i+1
	default:
		return m.endPending(i)
	}
	return i
}

// escSeqByte reads a byte of an ESC sequence after its first intermediate
// byte.
func (m *screenModel) escSeqByte(i int) int {
	rt := &m.rt
	switch b := rt.p[i]; {
	case b >= 0x20 && b < 0x30:
		rt.inters++
		if len(rt.held)+i-rt.s > screenMaxHeld {
			m.passOn(0x30)
		}
	case b >= 0x30 && b < 0x7f:
		if rt.inters == 1 {
			m.designate(rt.inter, b)
		}
		m.keepPending(i)
	default:
		return m.seqControl(i, 0x30)
	}
	return i
}

// wholeCSI looks for the common CSI: one that this read holds from p[i],
// after ESC [, to its final byte, with parameters and no other bytes. It
// returns the index of the final byte, or 0 if the CSI is not like that.
// Then the byte readers below read it.
func (m *screenModel) wholeCSI(i int) int {
	rt := &m.rt
	p := rt.p
	if len(rt.held) > 0 {
		return 0
	}
	prefix := byte(0)
	if i < len(p) && p[i] >= 0x3c && p[i] < 0x40 {
		prefix = p[i]
		i++
	}
	for i < len(p) && p[i] >= 0x30 && p[i] < 0x40 {
		i++
	}
	if i >= len(p) || p[i] < 0x40 || p[i] >= 0x7f {
		return 0
	}
	rt.prefix = prefix
	return i
}

// csiEntryByte reads the byte after ESC [.
func (m *screenModel) csiEntryByte(i int) int {
	rt := &m.rt
	switch b := rt.p[i]; {
	case b >= 0x30 && b < 0x40:
		rt.prefix = 0
		if b >= 0x3c {
			rt.prefix = b
		}
		rt.state = routeCSI
	case b >= 0x40 && b < 0x7f:
		rt.prefix = 0
		return m.csiEnd(i)
	case b >= 0x20 && b < 0x30:
		m.passOn(0x40)
	default:
		return m.seqControl(i, 0x40)
	}
	return i
}

// csiByte reads the parameters of a CSI, and the byte after them.
func (m *screenModel) csiByte(i int) int {
	rt := &m.rt
	p := rt.p
	for i < len(p) && p[i] >= 0x30 && p[i] < 0x40 {
		i++
	}
	if len(rt.held)+i-rt.s > screenMaxHeld {
		m.passOn(0x40)
		return i - 1
	}
	if i == len(p) {
		return i - 1
	}
	switch b := p[i]; {
	case b >= 0x40 && b < 0x7f:
		return m.csiEnd(i)
	case b >= 0x20 && b < 0x30:
		m.passOn(0x40)
		return i
	default:
		return m.seqControl(i, 0x40)
	}
}

// seqControl reads a byte that cannot be part of a CSI or of an ESC
// sequence with intermediate bytes. It does what vt's parser does with it.
// from is the lowest final byte of the sequence.
func (m *screenModel) seqControl(i int, from byte) int {
	rt := &m.rt
	switch b := rt.p[i]; {
	case b == 0x1b:
		// ESC drops the sequence and starts the next one.
		m.dropPending(i)
		rt.state = routeEsc
	case b == 0x7f:
		// vt skips DEL here.
	case b < 0x20 && b != 0x18 && b != 0x1a:
		// vt carries out the C0 control, and keeps its byte as the start of
		// the command. The final byte then joins that byte, so vt reads
		// another sequence than the one this looks like. Let vt read it, and
		// follow none of it.
		m.passOn(from)
	default:
		return m.endPending(i)
	}
	return i
}

// endPending reads CAN, SUB, or a byte from 0x80 on, which end the sequence
// that waits. vt drops the sequence, and then reads the byte as text.
func (m *screenModel) endPending(i int) int {
	m.dropPending(i)
	m.rt.state = routeText
	return i - 1
}

// csiEnd reads the final byte of a CSI with no intermediate bytes. It follows
// the sequences that switch screens, change a shared mode, or clear a tab
// stop.
func (m *screenModel) csiEnd(i int) int {
	rt := &m.rt
	rt.state = routeText
	seq := m.pendingSeq(i)
	final, params := seq[len(seq)-1], seq[2:len(seq)-1]
	if rt.prefix != 0 {
		params = params[1:]
	}
	on := final == 'h'
	switch {
	case rt.prefix == '?' && (on || final == 'l') && switchesScreen(params):
		// No emulator sees the switch itself.
		m.flushTo(rt.s)
		m.privateModes(params, on)
		rt.held = rt.held[:0]
		rt.r, rt.s = i+1, i+1
		return i
	case rt.prefix == '?' && (on || final == 'l'):
		m.followDECModes(params, on)
	case rt.prefix == 0 && (on || final == 'l'):
		m.followANSIModes(params, on)
	case rt.prefix == 0 && final == 'g':
		m.flushTo(rt.s)
		m.clearTabs(params)
	}
	m.keepPending(i)
	return i
}

// pendingSeq returns the sequence that waits, which ends at p[i], in one
// slice. If an earlier read cut it, its bytes in this read join the held
// ones. Held bytes start a read, so then r and s are equal.
func (m *screenModel) pendingSeq(i int) []byte {
	rt := &m.rt
	if len(rt.held) == 0 {
		return rt.p[rt.s : i+1]
	}
	rt.held = append(rt.held, rt.p[rt.s:i+1]...)
	rt.r, rt.s = i+1, i+1
	return rt.held
}

// keepPending lets the sequence that waits, which ends at p[i], go on as it
// is. A part that an earlier read cut goes on now. Otherwise the sequence
// stays in place, and goes on with the bytes after it.
func (m *screenModel) keepPending(i int) {
	rt := &m.rt
	rt.state = routeText
	if len(rt.held) > 0 {
		m.pass(m.pendingSeq(i))
		rt.held = rt.held[:0]
	}
}

// passOn lets the sequence that waits go on as it comes.
func (m *screenModel) passOn(from byte) {
	rt := &m.rt
	rt.state, rt.passFrom = routePass, from
	if len(rt.held) > 0 {
		m.pass(rt.held)
		rt.held = rt.held[:0]
	}
}

// dropPending drops the sequence that waits, after it passes on the text
// before it. The bytes from p[next] on are still to come.
func (m *screenModel) dropPending(next int) {
	rt := &m.rt
	m.flushTo(rt.s)
	rt.held = rt.held[:0]
	rt.r, rt.s = next, next
}

// flushTo passes on p[r:j].
func (m *screenModel) flushTo(j int) {
	rt := &m.rt
	if rt.r < j {
		m.pass(rt.p[rt.r:j])
		rt.r = j
	}
}

// passByte reads a byte of a sequence that goes on as it comes.
func (m *screenModel) passByte(i int) int {
	rt := &m.rt
	switch b := rt.p[i]; {
	case b >= rt.passFrom && b < 0x7f, b == 0x18, b == 0x1a:
		rt.state = routeText
	case b == 0x1b:
		// vt has seen part of the sequence. CAN drops it, before the next
		// sequence, which may go elsewhere.
		m.flushTo(i)
		m.pass(seqCAN)
		rt.state, rt.s = routeEsc, i
	case b >= 0x80 && b < 0xa0:
		m.flushTo(i)
		m.pass(seqCAN)
		rt.r = i + 1
		rt.state = routeText
	case b >= 0xa0:
		// vt drops the sequence, and reads the byte as text.
		rt.state = routeText
		return i - 1
	}
	return i
}

// stringBytes drops the bytes of a control string, up to its end.
func (m *screenModel) stringBytes(i int) int {
	rt := &m.rt
	p := rt.p
	for ; i < len(p); i++ {
		switch b := p[i]; {
		case b == 0x1b:
			rt.state = routeStringEsc
		case b == 0x07 && rt.osc, b == 0x18, b == 0x1a:
			rt.state = routeText
		default:
			continue
		}
		rt.r = i + 1
		return i
	}
	rt.r = len(p)
	return len(p) - 1
}

// stringEscByte reads the byte after an ESC inside a control string. ESC \
// (ST) ends the string. Any other byte ends it too, and the ESC starts the
// next sequence.
func (m *screenModel) stringEscByte(i int) int {
	rt := &m.rt
	if rt.p[i] == '\\' {
		rt.state, rt.r = routeText, i+1
		return i
	}
	rt.held = append(rt.held[:0], 0x1b)
	rt.state, rt.r, rt.s = routeEsc, i, i
	return i - 1
}
