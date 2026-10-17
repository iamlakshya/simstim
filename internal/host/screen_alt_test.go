package host

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/term/termtest"
)

// feedInPieces feeds a stream to the model in pieces of many sizes, so that
// the reads split its sequences in many places.
func feedInPieces(m *screenModel, stream []byte) {
	sizes := []int{1, 2, 3, 5, 8, 13, 64, 1000}
	for i := 0; len(stream) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(stream))
		m.feed(stream[:n])
		stream = stream[n:]
	}
}

// oneEmulator returns an emulator that got the whole stream in one write. Its
// scrollback of one line does not change what it shows.
func oneEmulator(t *testing.T, stream ...[]byte) *vt.Emulator {
	t.Helper()
	e := newScreenEmulator(80, 24)
	if e == nil {
		t.Fatal("the emulator cannot drop its replies")
	}
	_, _ = e.Write(bytes.Join(stream, nil))
	return e
}

// sameScreen fails the test unless the model shows the text that the
// emulator shows, with the cursor in the same place.
func sameScreen(t *testing.T, what string, m *screenModel, want *vt.Emulator) {
	t.Helper()
	got, exp := strings.Join(m.screen().Lines, "\n"), strings.Join(screenLines(want), "\n")
	if got != exp {
		t.Errorf("%s: the model shows\n%s\nwant\n%s", what, got, exp)
	}
	if c, w := m.current().CursorPosition(), want.CursorPosition(); c != w {
		t.Errorf("%s: the cursor is at %v, want %v", what, c, w)
	}
}

// cursorTo returns the CUP sequence that moves the cursor to pos.
func cursorTo(pos uv.Position) []byte {
	return fmt.Appendf(nil, "\x1b[%d;%dH", pos.Y+1, pos.X+1)
}

// After the alternate screen, the main screen shows the text of an emulator
// that never got the alternate screen's output. After 1049, the cursor is
// where it was at the switch, and vt itself shows the same. After 1047 and
// 47, the cursor is where the alternate screen left it, as in xterm.
func TestScreenModelMainScreenAfterTheAltScreen(t *testing.T) {
	before := append(termtest.InlineOutput(4<<10), "\x1b[5;10Hthe main screen"...)
	full := append([]byte("\x1b[2J\x1b[Hfull screen\r\n"), termtest.InlineOutput(64<<10)...)
	full = append(full, "\x1b[3;3Hthe alternate screen"...)
	after := []byte("\r\nback on the main screen")
	for _, mode := range []string{"1049", "1047", "47"} {
		t.Run(mode, func(t *testing.T) {
			on, off := []byte("\x1b[?"+mode+"h"), []byte("\x1b[?"+mode+"l")
			m := newScreenModel(80, 24)
			feedInPieces(m, bytes.Join([][]byte{before, on, full, off}, nil))
			if m.altScreen() {
				t.Fatal("the model is still on the alternate screen")
			}
			cursor := oneEmulator(t, before).CursorPosition()
			if mode != "1049" {
				cursor = oneEmulator(t, full).CursorPosition()
			}
			feedInPieces(m, after)
			sameScreen(t, "without the alternate screen", m, oneEmulator(t, before, cursorTo(cursor), after))
			if mode == "1049" {
				sameScreen(t, "vt", m, oneEmulator(t, before, on, full, off, after))
			}
		})
	}
}

// The modes in one sequence take effect in turn. Those before a switch go to
// the screen that showed, and those after it to the next screen. Mode 1048
// saves the cursor and restores it, for each screen on its own. So
// the cursor shows where each mode went, and vt itself gives the answer.
func TestScreenModelSwitchesInTurn(t *testing.T) {
	// Save the cursor at the top left, then move it.
	const setup = "\x1b[1;1H\x1b7\x1b[5;5H"
	cases := map[string]string{
		"a mode before the switch":                    "\x1b[?1048;1047h\x1b[10;10H\x1b[?1047;1048l\x1b[20;20H\x1b8",
		"a mode after the switch":                     "\x1b[?1047;1048h\x1b[10;10H\x1b[?1047l\x1b8",
		"a mode after the switch back":                "\x1b[?1047h\x1b[3;3H\x1b7\x1b[10;10H\x1b[?1047;1048l",
		"1049 saves the cursor of the main screen":    "\x1b[?1049h\x1b[10;10H\x1b[?1049l\x1b[20;20H\x1b8",
		"other modes on both sides of the switch":     "\x1b[?25;1049;2004h\x1b[10;10Hx\x1b[?2004;1049;25l",
		"the switch back on the main screen":          "\x1b[?1049l\x1b[?1047l\x1b[?47l\x1b8",
		"a switch to the alternate screen that shows": "\x1b[?1049hfull\x1b[?1049h\x1b[?1047h\x1b[?47h screen\x1b[3;3H\x1b[?1049h\x1b[9;9H\x1b8+",
	}
	for name, stream := range cases {
		stream := []byte(setup + stream + "end")
		m := newScreenModel(80, 24)
		feedInPieces(m, stream)
		one := oneEmulator(t, stream)
		sameScreen(t, name, m, one)
		if m.altScreen() != one.IsAltScreen() {
			t.Errorf("%s: the model is on the alternate screen: %v, want %v", name, m.altScreen(), one.IsAltScreen())
		}
	}
}

// xterm keeps one cursor for both screens. So after 1047 or 47, the cursor
// is where the alternate screen left it. 1049 restores the cursor that it
// saved, which is where the main screen left it.
func TestScreenModelCursorAfterTheAltScreen(t *testing.T) {
	for _, c := range []struct {
		name, stream string
		x, y         int
	}{
		{"1047", "main\x1b[?1047h\x1b[5;7Hfull\x1b[?1047l", 10, 4},
		{"47", "main\x1b[?47h\x1b[5;7Hfull\x1b[?47l", 10, 4},
		{"1049", "main\x1b[?1049h\x1b[5;7Hfull\x1b[?1049l", 4, 0},
		// 1048 before the switch back restores the alternate screen's cursor,
		// which the main screen then takes.
		{"a mode before the switch back", "\x1b[5;5H\x1b[?1047h\x1b[3;3H\x1b7\x1b[10;10H\x1b[?1048;1047l", 2, 2},
	} {
		m := newScreenModel(80, 24)
		feedInPieces(m, []byte(c.stream))
		if m.altScreen() {
			t.Fatalf("%s: the model is still on the alternate screen", c.name)
		}
		if pos := m.e.CursorPosition(); pos.X != c.x || pos.Y != c.y {
			t.Errorf("%s: the cursor is at %v, want (%d,%d)", c.name, pos, c.x, c.y)
		}
	}
}

// A terminal keeps autowrap and the G0 and G1 sets for both screens, and vt
// keeps them for its whole emulator. The model sets them in each emulator
// that starts to show, so the text lands where vt itself puts it.
func TestScreenModelSharesModesBetweenTheScreens(t *testing.T) {
	long := strings.Repeat("x", 100)
	cases := map[string]string{
		"autowrap off before the switch":           "\x1b[?7l\x1b[?1049h" + long,
		"autowrap off in the switch":               "\x1b[?7;1049l\x1b[?1049h" + long,
		"line drawing in G0 before the switch":     "\x1b(0\x1b[?1049hqqq",
		"line drawing in G3, after LS3":            "\x1b+0\x1bo\x1b[?1049hqqq",
		"autowrap on again, on the alternate side": "\x1b[?7l\x1b[?1049h\x1b[?7h\x1b[?1049l" + long,
		"autowrap off after the switch back":       "\x1b[?1049h\x1b[?1049;7l" + long,
		"ASCII again, on the alternate side":       "\x1b(0\x1b[?1049h\x1b(B\x1b[?1049lqqq",
		"LS2, on the alternate side":               "\x1b*0\x1b[?1049h\x1bn\x1b[?1049lqqq",
		"SO and SI, which vt does not carry out":   "\x1b)0\x0e\x1b[?1049hq\x0f\x1b[?1049lq",
		"RIS":                                      "\x1b[?7l\x1b(0\x1bc\x1b[?1049h" + long + "qqq",
		"new line mode":                            "\x1b[20h\x1b[?1049ha\nb",
		"tab stops":                                "\x1b[3g\x1b[1;20H\x1bH\x1b[1;1H\x1b[?1049h\tX",
		"tab stops, on the alternate side":         "\x1b[?1049h\x1b[3g\x1b[1;30H\x1bH\x1b[?1049l\r\tX",
		"one tab stop cleared":                     "\x1b[1;9H\x1b[g\x1b[1;1H\x1b[?1049h\tX",
		"origin mode":                              "\x1b[?6h\x1b[?1049h\x1b[5;10r\x1b[1;1HX",
		"G2, after LS2":                            "\x1b*0\x1bn\x1b[?1049hq",
		"grapheme widths":                          "\x1b[?2027h\x1b[?1049h👍🏽x",
	}
	for name, stream := range cases {
		m := newScreenModel(80, 24)
		feedInPieces(m, []byte(stream))
		sameScreen(t, name, m, oneEmulator(t, []byte(stream)))
	}
}

// The model uses its last alternate screen again, so that a program that
// switches often costs little. The screen shows nothing of its last use: no
// text, no scrollback, the cursor at the top left, and its modes and saved
// cursor reset.
func TestScreenModelReusesTheAltScreen(t *testing.T) {
	m := newScreenModel(80, 24)
	m.feed([]byte("\x1b[?1049hold text\r\n\x1b[2J\x1b[5;5H\x1b7\x1b[4h\x1b[?1049l"))
	spare := m.spare
	if spare == nil {
		t.Fatal("the model kept no spare emulator")
	}
	m.resize(100, 30)
	m.feed([]byte("\x1b[?1049h"))
	if m.alt != spare {
		t.Error("the model made a new emulator")
	}
	if w, h := m.alt.Width(), m.alt.Height(); w != 100 || h != 30 {
		t.Errorf("the alternate screen is %d × %d, want 100 × 30", w, h)
	}
	if text := strings.Join(m.screen().Lines, ""); text != "" {
		t.Errorf("the alternate screen shows %q, want nothing", text)
	}
	if n := m.alt.ScrollbackLen(); n != 0 {
		t.Errorf("the alternate screen keeps %d lines of scrollback, want none", n)
	}
	if pos := m.alt.CursorPosition(); pos.X != 0 || pos.Y != 0 {
		t.Errorf("the cursor is at %v, want (0,0)", pos)
	}
	// With insert mode still on, X would push ab to the right. With the old
	// saved cursor, DECRC would move Y to the fifth row.
	m.feed([]byte("ab\x1b[1;1HX\x1b8Y"))
	if got := m.screen().Lines[0]; got != "Yb" {
		t.Errorf("the first line is %q, want %q", got, "Yb")
	}
}

// A switch that a read splits still switches, once its last byte comes.
func TestScreenModelSwitchSplitAcrossReads(t *testing.T) {
	m := newScreenModel(80, 24)
	for _, step := range []struct {
		in   string
		alt  bool
		line string
	}{
		{"main\x1b[?10", false, "main"},
		{"49", false, "main"},
		{"hfull", true, "full"},
		{"\x1b", true, "full"},
		{"[?1049", true, "full"},
		{"l", false, "main"},
	} {
		m.feed([]byte(step.in))
		if m.altScreen() != step.alt {
			t.Fatalf("after %q, the model is on the alternate screen: %v, want %v", step.in, m.altScreen(), step.alt)
		}
		if got := m.screen().Lines[0]; got != step.line {
			t.Fatalf("after %q, the first line is %q, want %q", step.in, got, step.line)
		}
	}
}

// No emulator ever switches to its own alternate screen, whose scrollback
// vt keeps at 10,000 lines. The model's own alternate screen keeps one line.
func TestScreenModelEmulatorsStayOnTheirMainScreens(t *testing.T) {
	m := newScreenModel(80, 24)
	m.feed([]byte("\x1b[?25;1049;2004h\x1b[?47h\x1b[?1047h"))
	if m.alt == nil || m.e.IsAltScreen() || m.alt.IsAltScreen() {
		t.Fatal("an emulator switched to its own alternate screen")
	}
	chunk := termtest.InlineOutput(32 << 10)
	for range 4 {
		m.feed(chunk)
	}
	if n := m.alt.ScrollbackLen(); n > screenScrollback {
		t.Errorf("the alternate screen keeps %d lines of scrollback, want %d at most", n, screenScrollback)
	}
	if n := m.e.ScrollbackLen(); n != 0 {
		t.Errorf("the main screen got %d lines of the alternate screen's output", n)
	}
}

// A resize reaches both screens, and a new alternate screen takes the size
// that the main screen has.
func TestScreenModelResizesBothScreens(t *testing.T) {
	m := newScreenModel(80, 24)
	m.feed([]byte("\x1b[?1049h"))
	m.resize(100, 30)
	m.feed([]byte("\x1b[?1049l"))
	if w, h := m.e.Width(), m.e.Height(); w != 100 || h != 30 {
		t.Errorf("the main screen is %d × %d, want 100 × 30", w, h)
	}
	m.resize(120, 40)
	m.feed([]byte("\x1b[?1049h"))
	if w, h := m.alt.Width(), m.alt.Height(); w != 120 || h != 40 {
		t.Errorf("the alternate screen is %d × %d, want 120 × 40", w, h)
	}
	m.resize(90, 20)
	if w, h := m.alt.Width(), len(m.screen().Lines); w != 90 || h != 20 {
		t.Errorf("the alternate screen is %d × %d, want 90 × 20", w, h)
	}
}

// RIS resets the terminal, which then shows its main screen.
func TestScreenModelResetShowsTheMainScreen(t *testing.T) {
	m := newScreenModel(80, 24)
	m.feed([]byte("main\x1b[?1049hfull\x1bc"))
	if m.altScreen() {
		t.Fatal("after RIS, the model is still on the alternate screen")
	}
	m.feed([]byte("fresh"))
	if got := m.screen().Lines[0]; got != "fresh" {
		t.Errorf("after RIS, the first line is %q, want %q", got, "fresh")
	}
}

// The status rules read the alternate screen while it shows, also after the
// program scrolled it a long way.
func TestScreenModelRulesReadTheAltScreen(t *testing.T) {
	m := newScreenModel(80, 24)
	m.feed([]byte("$ opencode\r\n\x1b[?1049h"))
	m.feed(termtest.InlineOutput(256 << 10))
	m.feed([]byte("\x1b[2J\x1b[10;5H△ Permission required\x1b[12;5Hbash: ls"))
	if !m.altScreen() {
		t.Fatal("the model is not on the alternate screen")
	}
	if state, rule, _ := agents.Detect("opencode", m.screen()); state != "needs_input" {
		t.Errorf("on the alternate screen, the rules give %q (%s), want needs_input", state, rule)
	}
	m.feed([]byte("\x1b[?1049l"))
	if m.altScreen() {
		t.Fatal("the model is still on the alternate screen")
	}
	if state, rule, _ := agents.Detect("opencode", m.screen()); state == "needs_input" {
		t.Errorf("back on the main screen, the rules give needs_input (%s)", rule)
	}
	if got := m.screen().Lines[0]; got != "$ opencode" {
		t.Errorf("back on the main screen, the first line is %q, want %q", got, "$ opencode")
	}
}

// The emulators drop their replies to queries, so a query never blocks the
// model on either screen.
func TestScreenModelDropsReplies(t *testing.T) {
	m := newScreenModel(80, 24)
	if m.off {
		t.Fatal("the model cannot drop the emulator's replies")
	}
	queries := "\x1b[c\x1b[>c\x1b[5n\x1b[6n\x1b[?6n\x1b[?2004$p\x1b]11;?\x07"
	done := make(chan struct{})
	go func() {
		m.feed([]byte(queries + "\x1b[?1049h" + queries + "\x1b[?1049l" + queries))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a query blocked the model")
	}
	if m.off {
		t.Error("a query turned the model off")
	}
}
