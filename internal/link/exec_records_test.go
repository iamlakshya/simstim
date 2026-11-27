package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
)

func execRecord(ch byte, n uint32, p []byte) []byte {
	b := []byte{ch}
	b = binary.LittleEndian.AppendUint32(b, n)
	return append(b, p...)
}

func TestExecOutputSeparatesChannelsByteForByte(t *testing.T) {
	out := append([]byte{0, 0xff, 0x1b, 'c', '\r', '\n'}, bytes.Repeat([]byte("λ\x1b[?2026h"), 10000)...)
	errout := []byte("stderr\x00\r\n\x1b]52;c;data\a")
	var wire bytes.Buffer
	w := NewExecRecords(&wire)
	if n, err := w.Output(ExecStdout, out); err != nil || n != len(out) {
		t.Fatalf("stdout=%d,%v", n, err)
	}
	if _, err := w.Output(ExecStderr, errout); err != nil {
		t.Fatal(err)
	}
	if err := w.End(37); err != nil {
		t.Fatal(err)
	}
	var gotOut, gotErr bytes.Buffer
	code, err := ReadExecOutput(&wire, &gotOut, &gotErr)
	if err != nil || code != 37 || !bytes.Equal(out, gotOut.Bytes()) || !bytes.Equal(errout, gotErr.Bytes()) {
		t.Fatalf("code=%d err=%v, stdout/stderr bytes changed", code, err)
	}
}

func TestExecOutputRejectsMalformedTerminalAndData(t *testing.T) {
	goodEnd := execRecord(ExecEnd, 0, nil)
	cases := map[string][]byte{
		"no terminal":      execRecord(ExecStdout, 1, []byte("x")),
		"truncated header": {ExecStdout, 1},
		"truncated body":   execRecord(ExecStderr, 2, []byte("x")),
		"huge body":        execRecord(ExecStdout, ExecRecordMax+1, nil),
		"unknown channel":  execRecord(0xff, 0, nil),
		"bad status":       execRecord(ExecEnd, 256, nil),
		"duplicate end":    append(bytes.Clone(goodEnd), goodEnd...),
		"data after end":   append(bytes.Clone(goodEnd), execRecord(ExecStdout, 1, []byte("x"))...),
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			code, err := ReadExecOutput(bytes.NewReader(wire), io.Discard, io.Discard)
			if err == nil || code != 125 {
				t.Fatalf("code=%d err=%v", code, err)
			}
		})
	}
}

func TestExecRecordWriterRefusesLateOutputAndRepeatedEnd(t *testing.T) {
	var wire bytes.Buffer
	w := NewExecRecords(&wire)
	if err := w.End(0); err != nil {
		t.Fatal(err)
	}
	saved := bytes.Clone(wire.Bytes())
	if _, err := w.Output(ExecStdout, []byte("late")); err == nil {
		t.Fatal("late output accepted")
	}
	if err := w.End(0); err == nil {
		t.Fatal("duplicate end accepted")
	}
	if !bytes.Equal(saved, wire.Bytes()) {
		t.Fatal("failed write mutated wire")
	}
}

type shortExecWriter struct{ bytes.Buffer }

func (w *shortExecWriter) Write(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return w.Buffer.Write(p)
}
func TestExecShortWritesPreserveWholeRecord(t *testing.T) {
	w := new(shortExecWriter)
	records := NewExecRecords(w)
	_, _ = records.Output(ExecStdout, []byte("exact"))
	_ = records.End(0)
	output := new(shortExecWriter)
	code, err := ReadExecOutput(bytes.NewReader(w.Bytes()), output, nil)
	if code != 0 || err != nil || output.String() != "exact" {
		t.Fatalf("%d %v %q", code, err, output.String())
	}
}

type failedExecWriter struct{ calls int }

func (w *failedExecWriter) Write(p []byte) (int, error) { w.calls++; return 0, io.ErrClosedPipe }
func TestExecWriterLatchesPartialFrameFailure(t *testing.T) {
	target := new(failedExecWriter)
	w := NewExecRecords(target)
	_, err := w.Output(ExecStdout, []byte("x"))
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err = w.End(0); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("%d writes after frame failure", target.calls)
	}
}

func TestConcurrentExecChannelsNeverSpliceRecords(t *testing.T) {
	var wire bytes.Buffer
	w := NewExecRecords(&wire)
	var wg sync.WaitGroup
	for _, channel := range []byte{ExecStdout, ExecStderr} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if _, err := w.Output(channel, bytes.Repeat([]byte{channel}, 1000)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	_ = w.End(0)
	var out, errout bytes.Buffer
	code, err := ReadExecOutput(&wire, &out, &errout)
	if code != 0 || err != nil || !bytes.Equal(out.Bytes(), bytes.Repeat([]byte{ExecStdout}, 100000)) || !bytes.Equal(errout.Bytes(), bytes.Repeat([]byte{ExecStderr}, 100000)) {
		t.Fatalf("record splice: %d %v", code, err)
	}
}

type execInputSink struct {
	bytes.Buffer
	closed int
}

func (s *execInputSink) Close() error { s.closed++; return nil }
func TestExecStdinEOFRetainsSignalsAndHeartbeat(t *testing.T) {
	var wire bytes.Buffer
	w := NewExecRecords(&wire)
	_, _ = w.Input([]byte("\x1b[A\x00\r\n"))
	_ = w.CloseInput()
	_ = w.KeepAlive()
	_ = w.Signal(2)
	sink := new(execInputSink)
	cancelled := false
	var signals []uint32
	err := ReadExecInput(&wire, sink, func(sig uint32) error { signals = append(signals, sig); return nil }, func() { cancelled = true })
	if !errors.Is(err, io.EOF) || !cancelled || sink.String() != "\x1b[A\x00\r\n" || len(signals) != 1 || signals[0] != 2 {
		t.Fatalf("err=%v cancelled=%v data=%q signals=%v", err, cancelled, sink.String(), signals)
	}
}
func TestExecInputRejectsLateBytesAndForgedControl(t *testing.T) {
	cases := [][]byte{
		append(execRecord(ExecStdinEOF, 0, nil), execRecord(ExecStdin, 1, []byte("x"))...),
		execRecord(ExecSignal, 0, nil), execRecord(ExecSignal, 65, nil), execRecord(ExecSignal, 19, nil), execRecord(ExecAlive, 1, nil), execRecord(ExecStdinEOF, 1, nil), execRecord(ExecStdin, ExecRecordMax+1, nil), execRecord(ExecEnd, 0, nil),
	}
	for n, wire := range cases {
		cancelled := false
		err := ReadExecInput(bytes.NewReader(wire), new(execInputSink), func(uint32) error { return nil }, func() { cancelled = true })
		if err == nil || !cancelled {
			t.Fatalf("case%d err=%v cancel=%v", n, err, cancelled)
		}
	}
}

func FuzzExecOutputAlwaysBoundsAndTerminates(f *testing.F) {
	f.Add(execRecord(ExecEnd, 127, nil))
	f.Add(execRecord(ExecStdout, 0, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		code, err := ReadExecOutput(bytes.NewReader(data), io.Discard, io.Discard)
		if err != nil && code != 125 {
			t.Fatalf("protocol failure returned%d", code)
		}
		if err == nil && (code < 0 || code > 255) {
			t.Fatalf("unbounded status%d", code)
		}
	})
}
