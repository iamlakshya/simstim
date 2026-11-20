package client

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
	"github.com/iamlakshya/simstim/internal/term/termtest"
)

// As BenchmarkAttachOutputFrames, with one OSC 52 write of a new path in
// each frame. The paste guard notes each write before the frame goes on.
func BenchmarkAttachOutputFramesWithClipboardWrite(b *testing.B) {
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer out.Close()
	mem := clipMemoryAt(filepath.Join(b.TempDir(), "paste-guard.json"))
	a := &attacher{o: AttachOptions{Out: out}, tracker: term.NewTracker(), guard: newPasteGuard(mem)}
	agent := termtest.AgentOutput(32 << 10)
	b.SetBytes(int64(len(agent)))
	b.ReportAllocs()
	off := int64(0)
	for i := 0; b.Loop(); i++ {
		data := append(append([]byte(nil), agent...), osc52("/tmp/bench/"+strconv.Itoa(i))...)
		f := proto.Frame{Type: proto.TOutput, Payload: proto.EncodeOutput(off, data)}
		if _, err := a.frame(f); err != nil {
			b.Fatal(err)
		}
		off += int64(len(data))
	}
	mem.flush(10 * 1e9)
}
