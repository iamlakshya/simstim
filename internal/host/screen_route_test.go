package host

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

// noOwnAltScreen fails the test if an emulator of the model shows its own
// alternate screen, whose scrollback vt keeps at 10,000 lines.
func noOwnAltScreen(t *testing.T, what string, m *screenModel) {
	t.Helper()
	if m.e.IsAltScreen() || m.alt != nil && m.alt.IsAltScreen() {
		t.Errorf("%s: an emulator shows its own alternate screen", what)
	}
}

// The model drops control strings, and ends each one as a terminal does. vt
// ends a string at ESC as well, so an ESC that cut one used to let vt switch
// screens where the router saw no switch. vt also ends one at 0x9C, which is
// part of "✳".
func TestScreenModelDropsControlStrings(t *testing.T) {
	for _, c := range []struct {
		name, stream string
		alt          bool
		line         string
	}{
		{"an OSC that ESC cuts", "\x1b]0;title\x1b[?1049hfull", true, "full"},
		{"a DCS that ESC cuts", "\x1bPq#0;2;0;0;0\x1b[?1049hfull", true, "full"},
		{"an APC that ESC cuts", "\x1b_Gf=100;AAAA\x1b[?1049hfull", true, "full"},
		{"tmux passthrough", "\x1bPtmux;\x1b\x1b[?1049h\x1b\\full", true, "full"},
		{"ESC inside a CSI", "\x1b[?1\x1b[?1049hfull", true, "full"},
		{"a C0 control after ESC", "\x1b\r[?1049hfull", true, "full"},
		{"a C1 CSI byte", "\x9b?1049hfull", false, "?1049hfull"},
		{"a colon between modes", "\x1b[?1:1049hfull", true, "full"},
		{"a title with ✳ and BEL", "\x1b]0;✳ Claude Code\x07text", false, "text"},
		{"a title with ✳ and ST", "\x1b]2;✳ Claude Code\x1b\\text", false, "text"},
		{"text after an OSC that ESC cuts", "\x1b]0;title\x1b[1;1HDo you want to proceed?", false, "Do you want to proceed?"},
		{"CAN", "\x1b]0;title\x18text", false, "text"},
		{"BEL in a DCS", "\x1bPq\x07x\x1b\\text", false, "text"},
		{"0x9B and 0x9C in UTF-8 text", "✛ ✳", false, "✛ ✳"},
	} {
		m := newScreenModel(80, 24)
		feedInPieces(m, []byte(c.stream))
		if m.altScreen() != c.alt {
			t.Errorf("%s: the model is on the alternate screen: %v, want %v", c.name, m.altScreen(), c.alt)
		}
		noOwnAltScreen(t, c.name, m)
		if got := m.screen().Lines[0]; got != c.line {
			t.Errorf("%s: the first line is %q, want %q", c.name, got, c.line)
		}
		m.feed([]byte("\x1b[?1049l"))
		if m.altScreen() {
			t.Errorf("%s: the model stays on the alternate screen", c.name)
		}
	}
}

// vt carries out a C0 control inside a CSI, and then reads the CSI as
// another one: it joins the C0 byte to the final byte. So a switch with a C0
// control inside does not switch. The model lets vt read it.
func TestScreenModelLetsVTReadAGarbledCSI(t *testing.T) {
	for _, stream := range []string{
		"ab\x1b[?1049\rhfull",
		"ab\x1b[\r?1049hfull",
		"ab\x1b[8\rH",
		"ab\x1b[?\n1049h",
		"ab\x1b(\r0q",
	} {
		m := newScreenModel(80, 24)
		feedInPieces(m, []byte(stream))
		one := oneEmulator(t, []byte(stream))
		sameScreen(t, fmt.Sprintf("%q", stream), m, one)
		if m.altScreen() != one.IsAltScreen() {
			t.Errorf("%q: the model is on the alternate screen: %v, want %v", stream, m.altScreen(), one.IsAltScreen())
		}
	}
}

// The model follows the G0 to G3 sets only for the final bytes that vt
// knows. vt ignores ESC ( 1, so the line-drawing set stays in use.
func TestScreenModelFollowsOnlyKnownSets(t *testing.T) {
	stream := []byte("\x1b(0\x1b(1\x1b[?1049hm")
	m := newScreenModel(80, 24)
	m.feed(stream)
	sameScreen(t, "ESC ( 1", m, oneEmulator(t, stream))
}

// A long control string, such as an OSC 52 copy or a Sixel image, draws
// nothing, however long it is.
func TestScreenModelDropsALongString(t *testing.T) {
	for name, stream := range map[string][]byte{
		"OSC 52": bytes.Join([][]byte{
			[]byte("prompt> \x1b]52;c;"), bytes.Repeat([]byte("QUJD"), (70<<10)/4), []byte("\x07"),
		}, nil),
		"Sixel": bytes.Join([][]byte{
			[]byte("\x1b[?1049h\x1b[1;1HStatus: idle\x1b[2;1H\x1bPq"), bytes.Repeat([]byte("#0~~~~@@@@!10~-"), (80<<10)/15), []byte("\x1b\\"),
		}, nil),
	} {
		m := newScreenModel(80, 24)
		for rest := stream; len(rest) > 0; {
			k := min(4096, len(rest))
			m.feed(rest[:k])
			rest = rest[k:]
		}
		sameScreen(t, name, m, oneEmulator(t, stream))
	}
}

// If an emulator switches to its own alternate screen, on a sequence that
// the router did not see as a switch, it leaves that screen at once.
func TestScreenModelLeavesAMissedSwitch(t *testing.T) {
	m := newScreenModel(80, 24)
	m.pass([]byte("\x1b[?1049h"))
	noOwnAltScreen(t, "on the main screen", m)
	m.feed([]byte("\x1b[?1049h"))
	m.pass([]byte("\x1b[?1047h"))
	noOwnAltScreen(t, "on the alternate screen", m)
}

// matchesVT feeds random mixes of pieces to the model, in reads of 1 to
// maxRead bytes. After each stream, the model must show what one vt emulator
// shows after the whole stream, and no emulator its own alternate screen.
func matchesVT(t *testing.T, pieces []string, streams, maxRead int) {
	t.Helper()
	rnd := rand.New(rand.NewPCG(1, uint64(maxRead)))
	for n := range streams {
		var stream []byte
		for range 1 + rnd.IntN(150) {
			stream = append(stream, pieces[rnd.IntN(len(pieces))]...)
		}
		m := newScreenModel(80, 24)
		for rest := stream; len(rest) > 0; {
			k := min(1+rnd.IntN(maxRead), len(rest))
			m.feed(rest[:k])
			rest = rest[k:]
		}
		one := oneEmulator(t, stream)
		what := fmt.Sprintf("stream %d, %q", n, stream)
		sameScreen(t, what, m, one)
		if m.altScreen() != one.IsAltScreen() {
			t.Errorf("%s: the model is on the alternate screen: %v, want %v", what, m.altScreen(), one.IsAltScreen())
		}
		noOwnAltScreen(t, what, m)
		if t.Failed() {
			return
		}
	}
}

// For any mix of these pieces, the model shows what vt shows. The pieces
// leave out 1047, 47, RIS, scroll margins and DECRC: there the model follows
// a terminal, or keeps no state of an earlier alternate screen, where vt
// does.
func TestScreenModelMatchesVT(t *testing.T) {
	pieces := []string{
		"text ", "日本語", "✳", "→ ", "\r\n", "\r", "\n", "\t", "\b",
		"\x1b[31m", "\x1b[38;2;10;20;30m", "\x1b[0m", "\x1b[H", "\x1b[2J", "\x1b[K", "\x1b[5;10H", "\x1b[3A",
		"\x1b[?25l", "\x1b[?25h", "\x1b[?2004h", "\x1b[?2026h", "\x1b[?2026l",
		"\x1b[?1049h", "\x1b[?1049l", "\x1b[?25;1049h", "\x1b[?1049;25l", "\x1b[?1:1049h",
		"\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l", "\x1b[?69h", "\x1b[?69l", "\x1b[?2027h", "\x1b[?2027l",
		"\x1b[20h", "\x1b[20l", "\x1bH", "\x1b[g", "\x1b[3g", "\x1b[1;20H",
		"\x1b(0", "\x1b(B", "\x1b)0", "\x1b*0", "\x1b+0", "\x1b(1", "\x0e", "\x0f", "\x1bn", "\x1bo", "\x1b~", "\x1b}", "qx",
		"\x1b]0;a title\x07", "\x1b]8;;https://example.com\x1b\\", "\x1b]8;;\x1b\\", "\x1bP+q544e\x1b\\",
		"\x1b[c", "\x1b[6n", "\x1bM", "\x1bD",
	}
	matchesVT(t, pieces, 400, 40)
	matchesVT(t, pieces, 100, 1)
}

// Broken sequences too: an ESC, a CSI or a mode cut anywhere, and bytes
// that no UTF-8 character holds. A C0 control comes only after x, a final
// byte, so it never lands inside a sequence (TestScreenModelLetsVTReadAGarbledCSI).
func TestScreenModelMatchesVTOnBrokenSequences(t *testing.T) {
	pieces := []string{
		"ab", "日本", "✳", "x", "x\r\n", "x\x0e", "x\x0f", "x\t", "x\b",
		"\x18", "\x1a", "\x7f",
		"\x1b", "\x1b[", "\x1b[?", "?", ">", "=", "<", "$", " ", "!", "1049", "25", "7", "2004", ";", ":", "h", "l", "m",
		"5;5H", "p", "q", "(", ")", "0", "B", "A", "#",
		"\x1b[?1049h", "\x1b[?1049l", "\x1b[?7l", "\x1b[?7h", "\x1b(0", "\x1b(B", "\x1b)0", "\x1b[2J", "\x1b[H", "\x1b[5;10H",
		"\xa0", "\xff", "\xc0", "\xe2", "\xc3", "\xf0",
	}
	matchesVT(t, pieces, 400, 7)
	matchesVT(t, pieces, 200, 1)
}

// Whole control strings of every kind draw nothing, as in vt.
func TestScreenModelMatchesVTWithControlStrings(t *testing.T) {
	pieces := []string{
		"ab", "日本", "x", "x\r\n", "x\t", "\x1b[2J", "\x1b[H", "\x1b[5;10H", "\x1b[?25l", "\x1b[31m", "\x1b[0m",
		"\x1b[?1049h", "\x1b[?1049l", "\x1b[?7l", "\x1b[?7h", "\x1b(0", "\x1b(B", "qx",
		"\x1b]0;a title\x07", "\x1b]2;a title\x1b\\", "\x1b]8;;https://example.com\x1b\\", "\x1b]8;;\x1b\\",
		"\x1b]8;id=1;https://e.com/?a=b\x07link\x1b]8;;\x07",
		"\x1bP+q544e\x1b\\", "\x1b_Gf=100,a=T;AAAA\x1b\\", "\x1bPq#0;2;0;0;0#0~~@@-!10~\x1b\\", "\x1b^pm\x1b\\", "\x1bXsos\x1b\\",
		"\x1b]52;c;SGVsbG8=\x07", "\x1b]1337;File=inline=1:QUJD\x07", "\x1b]133;A\x07", "\x1b]9;note\x07", "\x1b]777;notify;a;b\x1b\\",
	}
	matchesVT(t, pieces, 400, 7)
	matchesVT(t, pieces, 100, 1)
}
