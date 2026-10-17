package host

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/term/termtest"
)

// The model keeps no more scrollback than screenScrollback, however long the
// agent's transcript grows. vt v0.1.0 kept 10,000 lines when asked for none,
// so this feeds three times as many.
func TestScreenModelKeepsNoScrollback(t *testing.T) {
	m := newScreenModel(80, 24)
	chunk := termtest.InlineOutput(32 << 10)
	perChunk := bytes.Count(chunk, []byte("\n"))
	for fed := 0; fed < 30000; fed += perChunk {
		m.feed(chunk)
		if n := m.e.ScrollbackLen(); n > screenScrollback {
			t.Fatalf("after %d lines, the model keeps %d lines of scrollback, want %d at most",
				fed+perChunk, n, screenScrollback)
		}
	}
	// The visible screen still holds the newest lines, for the status rules.
	s := m.screen()
	if len(s.Lines) != 24 {
		t.Fatalf("the screen has %d lines, want 24", len(s.Lines))
	}
	if !strings.Contains(strings.Join(s.Lines, "\n"), "holder.go") {
		t.Errorf("the screen lost the transcript:\n%s", strings.Join(s.Lines, "\n"))
	}
	// A cleared screen pushes its lines to the scrollback too.
	m.feed([]byte("\x1b[2J"))
	if n := m.e.ScrollbackLen(); n > screenScrollback {
		t.Errorf("after a clear, the model keeps %d lines of scrollback, want %d at most", n, screenScrollback)
	}
}

// altScreenMaxKept is the most heap that a model may keep after a long
// transcript on the alternate screen. Each emulator keeps about 4.5 MiB, most
// of it a buffer that vt's parser makes at the start.
const altScreenMaxKept = 16 << 20

// A full-screen program that scrolls the alternate screen keeps the model
// small too. vt v0.1.0 kept 10,000 lines of that screen, about 62 MiB, so
// this scrolls three times as many.
func TestScreenModelKeepsLittleOnTheAltScreen(t *testing.T) {
	base := liveHeap()
	m := newScreenModel(80, 24)
	m.feed([]byte("\x1b[?1049h"))
	chunk := termtest.InlineOutput(32 << 10)
	perChunk := bytes.Count(chunk, []byte("\n"))
	for fed := 0; fed < 30000; fed += perChunk {
		m.feed(chunk)
	}
	// A clear pushes the screen's lines to the scrollback too.
	m.feed([]byte("\x1b[2J"))
	if kept := liveHeap() - base; kept > altScreenMaxKept {
		t.Errorf("after 30,000 lines on the alternate screen, the model keeps %.1f MiB, want %d MiB at most",
			float64(kept)/(1<<20), altScreenMaxKept>>20)
	}
	if !m.altScreen() {
		t.Error("the model left the alternate screen")
	}
	runtime.KeepAlive(m)
}
