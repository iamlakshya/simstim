package host

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/term/termtest"
)

// The holder feeds each chunk of the agent's output to its screen model,
// and reads the screen on each tick to judge the agent's state.

func BenchmarkScreenModelFeed(b *testing.B) {
	data := termtest.AgentOutput(32 << 10)
	m := newScreenModel(120, 40)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		m.feed(data)
	}
}

// BenchmarkScreenModelScroll feeds an agent's transcript, which scrolls the
// screen, to a model whose scrollback is already full. It also reports the
// heap that the model keeps, in MiB.
func BenchmarkScreenModelScroll(b *testing.B) {
	benchScroll(b, nil)
}

// BenchmarkScreenModelAltScroll does the same on the alternate screen, as a
// full-screen agent does when it scrolls that screen.
func BenchmarkScreenModelAltScroll(b *testing.B) {
	benchScroll(b, []byte("\x1b[?1049h"))
}

// benchScroll feeds start, then a transcript that scrolls the screen.
func benchScroll(b *testing.B, start []byte) {
	data := termtest.InlineOutput(32 << 10)
	base := liveHeap()
	m := newScreenModel(80, 24)
	m.feed(start)
	// 30 chunks are more than 10,000 lines, the most that vt keeps.
	for range 30 {
		m.feed(data)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		m.feed(data)
	}
	b.ReportMetric(float64(liveHeap()-base)/(1<<20), "MiB-kept")
	runtime.KeepAlive(m)
}

// BenchmarkScreenModelSwitch feeds a program that switches to the alternate
// screen and back for each character it draws: 32 KiB of
// ESC [ ? 1049 h x ESC [ ? 1049 l.
func BenchmarkScreenModelSwitch(b *testing.B) {
	data := bytes.Repeat([]byte("\x1b[?1049hx\x1b[?1049l"), (32<<10)/18)
	m := newScreenModel(80, 24)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		m.feed(data)
	}
}

// liveHeap returns the bytes that the heap holds after a full collection.
func liveHeap() int64 {
	runtime.GC()
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return int64(s.HeapAlloc)
}

func BenchmarkScreenDetect(b *testing.B) {
	m := newScreenModel(120, 40)
	m.feed(termtest.AgentOutput(32 << 10))
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = agents.Detect("claude", m.screen())
	}
}
