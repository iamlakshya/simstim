package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher opens where the last run left off, with the sessions it saw,
// before any host has answered.
func TestMemoryOpensWhereYouLeftOff(t *testing.T) {
	be := labBackend()
	be.mem = memory{
		Sessions: map[string][]client.Session{"gcp-dev": be.state["gcp-dev"].sessions},
		Last:     &lastSession{Host: "gcp-dev", ID: "g1", Name: "bench"},
	}
	m := New(context.Background(), be, Options{Now: func() time.Time { return now }})
	if m.screen != scrSessions || m.cur != "gcp-dev" {
		t.Fatalf("opened on %v %q, want gcp-dev's sessions", m.screen, m.cur)
	}
	if s := m.selected(); s == nil || s.ID != "g1" {
		t.Fatalf("selected %v before any host answered, want the last session", s)
	}
	m.width, m.height = 80, 24
	if out := ansi.Strip(m.render()); !strings.Contains(out, "bench") || !strings.Contains(out, "last seen before this run") {
		t.Fatalf("the remembered sessions do not show at once:\n%s", out)
	}

	hs := newHarness(t, be, 80, 24)
	if hs.m.sel != (sessKey{"gcp-dev", "g1"}) {
		t.Fatalf("after the host answered the selection moved to %v", hs.m.sel)
	}
	hs.key("enter")
	if len(be.attached) != 1 || be.attached[0].Session != "g1" {
		t.Fatal("Enter did not take you straight back in")
	}
}

func TestAttachAndCloseRemember(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("enter")
	if l := hs.be.mem.Last; l == nil || l.Host != "atlas-dev" || l.ID != "s4" {
		t.Fatalf("the last session is %+v", l)
	}
	hs.m.Close()
	if n := len(hs.be.mem.Sessions["atlas-dev"]); n != 4 {
		t.Fatalf("closing saved %d sessions of atlas-dev, want 4", n)
	}
	if _, ok := hs.be.mem.Sessions["aws-gpu"]; ok {
		t.Fatal("a host that never answered was saved with sessions")
	}
}

func TestQuickOpen(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("3")
	if hs.m.screen != scrSessions || hs.m.cur != "gcp-dev" {
		t.Fatalf("3 opened %q", hs.m.cur)
	}
	hs.key("1")
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != "g1" {
		t.Fatalf("1 attached %+v", hs.be.attached)
	}
	if !strings.Contains(plainScreen(hs.m), "1 "+glyphSpinStill+" bench") {
		t.Fatal("rows do not show their numbers")
	}
}

func TestMouse(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.m.render()
	g := hs.m.geo
	click := func(x, y int) { hs.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) }
	click(g.x+10, g.listY+1)
	if hs.m.sel.id != "s3" || len(hs.be.attached) != 0 {
		t.Fatalf("a click on the second row selected %v", hs.m.sel)
	}
	click(g.x+10, g.listY+1)
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != "s3" {
		t.Fatal("a click on the selected row did not attach")
	}
	hs.send(tea.MouseWheelMsg{X: g.x + 10, Y: g.listY, Button: tea.MouseWheelDown})
	if hs.m.sel.id != "s2" {
		t.Fatalf("the wheel moved to %v", hs.m.sel)
	}
	hs.key("ctrl+o")
	hs.m.render()
	click(hs.m.modalGeo.x+10, hs.m.modalGeo.y+4) // the first command: new session
	if hs.m.modal != nil || hs.m.prompt == nil || hs.m.prompt.mode != promptNew {
		t.Fatalf("a click on a palette row ran nothing: modal %T, prompt %+v", hs.m.modal, hs.m.prompt)
	}
}

func TestTitleCountsTheSessionsThatNeedYou(t *testing.T) {
	hs := home(t, 80, 24)
	if got := hs.m.View().WindowTitle; !strings.HasPrefix(got, "● 1 · ") {
		t.Fatalf("title %q", got)
	}
}

func TestNotifyWhenNobodyIsLooking(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.send(tea.BlurMsg{})
	h := hs.be.state["atlas-dev"]
	hs.be.mu.Lock()
	h.sessions[2].State, h.sessions[2].Reason = api.StateNeedsInput, "Run the tests?"
	hs.be.mu.Unlock()
	notes := func() []string {
		var out []string
		for _, r := range hs.raw {
			if strings.Contains(r, "\x1b]777;notify;") {
				out = append(out, r)
			}
		}
		return out
	}
	hs.fire(func(m tea.Msg) bool { _, ok := m.(refreshTickMsg); return ok })
	if n := notes(); len(n) != 1 || !strings.Contains(n[0], "api-tests needs you: Run the tests?") {
		t.Fatalf("notifications %q", n)
	}
	hs.send(tea.FocusMsg{})
	hs.be.mu.Lock()
	h.sessions[1].State = api.StateNeedsInput
	hs.be.mu.Unlock()
	hs.fire(func(m tea.Msg) bool { _, ok := m.(refreshTickMsg); return ok })
	if len(notes()) != 1 {
		t.Fatal("a notification went out while the terminal had focus")
	}
}

func TestFilterHighlights(t *testing.T) {
	if got := fuzzyPos("atr", "auth-refactor"); got == nil || !got[0] || !got[2] || !got[5] {
		t.Fatalf("fuzzyPos = %v", got)
	}
	if fuzzyPos("zz", "auth") != nil || fuzzyPos("", "auth") != nil {
		t.Fatal("no match, or no query, should mark nothing")
	}
}

func TestTermColors(t *testing.T) {
	var tc termColors
	tc.onOSC("\x1b]4;3;rgb:f9f9/afaf/4f4f\x1b\\")
	c, ok := tc.resolve(lipgloss.Yellow, true)
	if !ok || c != (rgb{0xf9, 0xaf, 0x4f}) {
		t.Fatalf("slot 3 = %v %v", c, ok)
	}
	if _, ok := tc.resolve(nil, true); ok {
		t.Fatal("the default colour resolved before the terminal said it")
	}
	if _, ok := tc.resolve(lipgloss.Blue, true); ok {
		t.Fatal("an unknown slot resolved")
	}
}

func TestScanRuns(t *testing.T) {
	m := &Model{motion: true, now: func() time.Time { return now }, epoch: now}
	c := lipgloss.NewCanvas(40, 1)
	uvDraw(c, sFaint.Render(strings.Repeat("─", 40)))
	m.scan(c, 0, 0, 40)
	lit := func() int {
		n := 0
		for x := range 40 {
			if c.CellAt(x, 0).Style.Fg == colAccent {
				n++
			}
		}
		return n
	}
	first := lit()
	m.now = func() time.Time { return now.Add(glowLap / 2) }
	c = lipgloss.NewCanvas(40, 1)
	uvDraw(c, sFaint.Render(strings.Repeat("─", 40)))
	m.scan(c, 0, 0, 40)
	if lit() == 0 || c.CellAt(20, 0).Style.Fg != colAccent {
		t.Fatalf("half a lap in, the band is not in the middle (lit at start: %d)", first)
	}
}

// The logo moves only with a frame tick (docs/contributing/ux-spec.md 4.5).
// On a still list, the refresh, the activity read, the latency probe and
// their replies draw the screen again with the logo where it was. With frame
// ticks it goes on from there, and each column moves one row at most with
// each frame.
func TestLogoMovesOnlyWithFrames(t *testing.T) {
	be := labBackend()
	for _, h := range be.state {
		for i := range h.sessions {
			h.sessions[i].State = api.StateIdle
		}
	}
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = true
	hs.key("enter") // atlas-dev's sessions slide in
	logo := func() string {
		line := strings.TrimSpace(strings.Split(plainScreen(hs.m), "\n")[headerY])
		return string([]rune(line)[:3])
	}
	isFrame := func(msg tea.Msg) bool { _, ok := msg.(animTickMsg); return ok }
	// frame delivers the pending frame tick when it is due, if there is one.
	frame := func() bool {
		for _, tm := range hs.timers {
			if isFrame(tm.msg) {
				hs.clock = hs.clock.Add(tm.d)
				return hs.fire(isFrame) == 1
			}
		}
		return false
	}
	isTimer := func(msg tea.Msg) bool {
		switch msg.(type) {
		case refreshTickMsg, activityTickMsg, latencyTickMsg:
			return true
		}
		return false
	}
	settle := func() { // lets every sequence end
		for i := 0; frame(); i++ {
			if i > 1000 {
				t.Fatal("the frame ticks never stop")
			}
		}
	}
	hs.clock = hs.clock.Add(time.Minute)
	hs.fire(isTimer) // the first activity line decodes in
	settle()

	still := logo()
	for range 3 {
		hs.clock = hs.clock.Add(latencyEvery)
		if n := hs.fire(isTimer); n != 3 {
			t.Fatalf("fired %d timers, want the refresh, the activity read and the latency probe", n)
		}
		if frame() {
			t.Fatal("a frame ticks on a still list")
		}
		if got := logo(); got != still {
			t.Fatalf("with no frame tick the logo moved from %q to %q", still, got)
		}
	}

	// A session starts to work: the frames tick, and the logo moves with them.
	rows := func(s string) (out [6]int) {
		for c, r := range []rune(s) {
			for dx := range 2 {
				for y := range 4 {
					if (r-0x2800)&(1<<brailleBit[y][dx]) != 0 {
						out[c*2+dx] = y
					}
				}
			}
		}
		return out
	}
	be.mu.Lock()
	be.state["atlas-dev"].sessions[2].State = api.StateWorking
	be.mu.Unlock()
	hs.fire(func(msg tea.Msg) bool { _, ok := msg.(refreshTickMsg); return ok })
	last, moved := still, false
	for range 20 {
		if !frame() {
			t.Fatal("a working session does not tick")
		}
		got := logo()
		a, b := rows(last), rows(got)
		for i := range a {
			if a[i]-b[i] > 1 || b[i]-a[i] > 1 {
				t.Fatalf("one frame took the logo from %q to %q, more than one dot", last, got)
			}
		}
		moved = moved || got != still
		last = got
	}
	if !moved {
		t.Fatalf("the logo stayed at %q through 20 frames", still)
	}
}

func TestLifeFrames(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range lifeFrames {
		r := []rune(f)
		if len(r) != 1 || r[0] < 0x2801 || r[0] > 0x28FF {
			t.Fatalf("frame %q is not a braille cell with dots", f)
		}
		seen[f] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d different frames", len(seen))
	}
}

func TestActivityLine(t *testing.T) {
	screen := "\x1b[2J\x1b[H" +
		"> Make the API tests pass\r\n\r\n" +
		"  ran go test ./... (3 failed)\r\n" +
		"∴ Working (14s · esc to interrupt)\r\n" +
		"╭──────────────────────────╮\r\n" +
		"│ >                        │\r\n" +
		"╰──────────────────────────╯\r\n" +
		"  ? for shortcuts      ~/src/api\r\n"
	if got := activityLine(renderScreen([]byte(screen), 40, 10)); got != "∴ Working (14s · esc to interrupt)" {
		t.Fatalf("activity %q", got)
	}
	if got := activityLine(renderScreen([]byte("\x1b[2J"), 40, 10)); got != "" {
		t.Fatalf("a blank screen says %q", got)
	}
}

func TestTraceRisesAndFalls(t *testing.T) {
	m := &Model{now: func() time.Time { return now }, traces: map[sessKey][]time.Time{}}
	k := sessKey{"h", "s"}
	flat := ansi.Strip(m.trace(k, sPlain))
	if flat != strings.Repeat("⣀", traceCells) {
		t.Fatalf("a quiet session's trace is %q", flat)
	}
	m.traces[k] = []time.Time{now.Add(-time.Second), now}
	busy := ansi.Strip(m.trace(k, sPlain))
	if busy == flat || []rune(busy)[traceCells-1] == '⣀' {
		t.Fatalf("output just now does not raise the end of the trace: %q", busy)
	}
	m.now = func() time.Time { return now.Add(time.Minute) }
	if got := ansi.Strip(m.trace(k, sPlain)); got != flat {
		t.Fatalf("a minute later the trace is %q, not flat", got)
	}
}
