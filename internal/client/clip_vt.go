package client

import "bytes"

// The paste guard parses the stream as a terminal does, to know where an
// OSC string starts and ends. It has two readings (spec 7.6). vtNext is the
// state machine of the Ghostty commit that the shadow terminal pins
// (src/terminal/parse_table.zig and stream.zig). vtNextX is xterm's with C1
// controls on: the reader first turns each C1 control into its 7-bit ESC
// form (clip_c1.go), so every byte from 0x80 left is text there.
//
// In ground, Ghostty decodes UTF-8, and only ESC changes the state: a C1
// control decoded from UTF-8 is ignored. Outside ground it reads raw bytes,
// so a raw 0x9D starts an OSC string from every state but ground, OSC and
// DCS passthrough. In the escape state, C0 controls, DEL and bytes from
// 0xA0 keep the state, so "ESC CR ]" starts an OSC string too.

const (
	vtGround uint8 = iota
	vtEscape
	vtEscInter
	vtCSIEntry
	vtCSIParam
	vtCSIInter
	vtCSIIgnore
	vtDCSEntry
	vtDCSParam
	vtDCSInter
	vtDCSPass
	vtDCSIgnore
	vtOSC
	vtString // SOS, PM and APC
	vtStates
)

// vtNext is the next state for each state and byte.
var vtNext = func() (t [vtStates][256]uint8) {
	for s := range vtStates {
		for c := range 256 {
			t[s][c] = s // a byte that the table does not name keeps the state
		}
		// From anywhere.
		set(&t, s, 0x18, 0x18, vtGround)
		set(&t, s, 0x1A, 0x1A, vtGround)
		set(&t, s, 0x80, 0x8F, vtGround)
		set(&t, s, 0x91, 0x97, vtGround)
		set(&t, s, 0x99, 0x9A, vtGround)
		set(&t, s, 0x9C, 0x9C, vtGround)
		set(&t, s, 0x1B, 0x1B, vtEscape)
		set(&t, s, 0x98, 0x98, vtString)
		set(&t, s, 0x9E, 0x9F, vtString)
		set(&t, s, 0x9B, 0x9B, vtCSIEntry)
		set(&t, s, 0x90, 0x90, vtDCSEntry)
		set(&t, s, 0x9D, 0x9D, vtOSC)
	}
	keep := func(s uint8, ranges ...[2]byte) {
		for _, r := range ranges {
			set(&t, s, r[0], r[1], s)
		}
	}
	c0 := [][2]byte{{0x00, 0x17}, {0x19, 0x19}, {0x1C, 0x1F}}
	for _, s := range []uint8{vtEscape, vtEscInter, vtCSIEntry, vtCSIParam, vtCSIInter, vtCSIIgnore,
		vtDCSEntry, vtDCSParam, vtDCSInter, vtDCSIgnore, vtDCSPass, vtString} {
		keep(s, c0...)
	}
	keep(vtEscInter, [2]byte{0x20, 0x2F}, [2]byte{0x7F, 0x7F})
	set(&t, vtEscInter, 0x30, 0x7E, vtGround)
	keep(vtString, [2]byte{0x20, 0x7F})

	keep(vtEscape, [2]byte{0x7F, 0x7F})
	set(&t, vtEscape, 0x30, 0x7E, vtGround)
	set(&t, vtEscape, 0x20, 0x2F, vtEscInter)
	set(&t, vtEscape, 'X', 'X', vtString)
	set(&t, vtEscape, '^', '_', vtString)
	set(&t, vtEscape, 'P', 'P', vtDCSEntry)
	set(&t, vtEscape, '[', '[', vtCSIEntry)
	set(&t, vtEscape, ']', ']', vtOSC)

	keep(vtDCSEntry, [2]byte{0x7F, 0x7F})
	set(&t, vtDCSEntry, 0x20, 0x2F, vtDCSInter)
	set(&t, vtDCSEntry, 0x30, 0x3F, vtDCSParam)
	set(&t, vtDCSEntry, 0x3A, 0x3A, vtDCSIgnore)
	set(&t, vtDCSEntry, 0x40, 0x7E, vtDCSPass)
	keep(vtDCSInter, [2]byte{0x20, 0x2F}, [2]byte{0x7F, 0x7F})
	set(&t, vtDCSInter, 0x30, 0x3F, vtDCSIgnore)
	set(&t, vtDCSInter, 0x40, 0x7E, vtDCSPass)
	keep(vtDCSIgnore, [2]byte{0x80, 0xFF})
	keep(vtDCSParam, [2]byte{0x30, 0x39}, [2]byte{0x3B, 0x3B}, [2]byte{0x7F, 0x7F})
	set(&t, vtDCSParam, 0x3A, 0x3A, vtDCSIgnore)
	set(&t, vtDCSParam, 0x3C, 0x3F, vtDCSIgnore)
	set(&t, vtDCSParam, 0x20, 0x2F, vtDCSInter)
	set(&t, vtDCSParam, 0x40, 0x7E, vtDCSPass)
	keep(vtDCSPass, [2]byte{0x20, 0x7F}, [2]byte{0x80, 0xFF})

	keep(vtCSIParam, [2]byte{0x30, 0x3B}, [2]byte{0x7F, 0x7F})
	set(&t, vtCSIParam, 0x40, 0x7E, vtGround)
	set(&t, vtCSIParam, 0x3C, 0x3F, vtCSIIgnore)
	set(&t, vtCSIParam, 0x20, 0x2F, vtCSIInter)
	keep(vtCSIIgnore, [2]byte{0x20, 0x3F}, [2]byte{0x7F, 0x7F})
	set(&t, vtCSIIgnore, 0x40, 0x7E, vtGround)
	keep(vtCSIInter, [2]byte{0x20, 0x2F}, [2]byte{0x7F, 0x7F})
	set(&t, vtCSIInter, 0x40, 0x7E, vtGround)
	set(&t, vtCSIInter, 0x30, 0x3F, vtCSIIgnore)
	keep(vtCSIEntry, [2]byte{0x7F, 0x7F})
	set(&t, vtCSIEntry, 0x40, 0x7E, vtGround)
	set(&t, vtCSIEntry, 0x3A, 0x3A, vtCSIIgnore)
	set(&t, vtCSIEntry, 0x20, 0x2F, vtCSIInter)
	set(&t, vtCSIEntry, 0x30, 0x39, vtCSIParam)
	set(&t, vtCSIEntry, 0x3B, 0x3F, vtCSIParam)

	// An OSC string ends at BEL and ST (ESC \). CAN and SUB abort it. Other C0
	// controls are ignored, and every other byte is its text, 0x9C too.
	keep(vtOSC, [2]byte{0x00, 0x06}, [2]byte{0x08, 0x17}, [2]byte{0x19, 0x19}, [2]byte{0x1C, 0xFF})
	set(&t, vtOSC, 0x07, 0x07, vtGround)

	// Ground is not in the table: there, only ESC changes the state (vtStep).
	return t
}()

// vtNextX is xterm's reading, after its C1 controls became ESC forms: a
// byte from 0x80 changes no state.
var vtNextX = func() (t [vtStates][256]uint8) {
	t = vtNext
	for s := range vtStates {
		set(&t, s, 0x80, 0xFF, s)
	}
	return t
}()

// vtTable is a reading's state machine.
type vtTable = *[vtStates][256]uint8

func set(t *[vtStates][256]uint8, s uint8, from, to byte, next uint8) {
	for c := int(from); c <= int(to); c++ {
		t[s][c] = next
	}
}

func vtStep(t vtTable, s, c uint8) uint8 {
	if s == vtGround {
		if c == 0x1b {
			return vtEscape
		}
		return vtGround
	}
	return t[s][c]
}

// vtUntil reads b from state s, which is neither ground nor OSC, until the
// state is ground or an OSC string starts. It returns that state and the
// bytes it read.
func vtUntil(t vtTable, s uint8, b []byte) (uint8, int) {
	for i, c := range b {
		n := t[s][c]
		if n == vtGround || n == vtOSC {
			return n, i + 1
		}
		s = n
	}
	return s, len(b)
}

// vtEnd is the state after b, read from ground, when b holds no byte that
// starts an OSC string. Only an ESC leaves ground, so it reads from the last.
func vtEnd(t vtTable, b []byte) uint8 {
	k := bytes.LastIndexByte(b, 0x1b)
	if k < 0 {
		return vtGround
	}
	s, n := vtEscape, k+1
	for n < len(b) && s != vtGround {
		var used int
		s, used = vtUntil(t, s, b[n:])
		n += used
	}
	return s
}
