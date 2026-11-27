package client

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/iamlakshya/simstim/internal/brand"
)

func sameNotifyState(a, b *outStream) bool {
	return a.raw == b.raw && a.xterm == b.xterm && bytes.Equal(a.c1.pend, b.c1.pend)
}
func checkNotifyBuffers(t *testing.T, s *outStream) {
	t.Helper()
	if cap(s.c1.pend) > 8 || cap(s.c1.in) > 8 || cap(s.c1.out) > 8 {
		t.Fatalf("decoder retained frame-sized buffers: %d %d %d", cap(s.c1.pend), cap(s.c1.in), cap(s.c1.out))
	}
}

func notificationStreams() [][]byte {
	return [][]byte{
		[]byte("plain ❯ Ü ✔ 😀 text"),
		[]byte("\x1b]52;c;QUFB\x07OK"), []byte("\x1b]52;c;A\x18OK"), []byte("\x1b]52;c;A\x1aOK"), []byte("\x1b]52;c;A\x1b\\OK"),
		[]byte("\x1b]52;c;A\x1b[\x072JOK"), []byte("\x1bPqabc\x1b[0mOK"), []byte("\x1b_abc\x1b[0mOK"), []byte("\x1b[12;3\x1b]2;title\x07OK"),
		[]byte("\x9d52;c;QUFB\x9cOK"), []byte("\xc2\x9d52;c;QUFB\xc2\x9cOK"),
		{0xe2, 0xc2, 0x9d, 'A', 0x1b, '\\'}, {0xe2, 0x9d, 'O', 0x1b, '\\'}, {0xf0, 0x90, 0x80, 'A', 0x1b, '\\'},
	}
}

func TestNotificationFastStateMatchesByteState(t *testing.T) {
	streams := notificationStreams()
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	streams = append(streams, all)
	for i, stream := range streams {
		for _, chunk := range []int{1, 2, 3, 7, len(stream)} {
			var fast, slow outStream
			for start := 0; start < len(stream); start += chunk {
				part := stream[start:min(len(stream), start+chunk)]
				fast.feed(part)
				for _, c := range part {
					slow.step(c)
				}
				if !sameNotifyState(&fast, &slow) {
					t.Fatalf("stream%d chunk%d offset%d: fast=(%d,%d,%x) slow=(%d,%d,%x)", i, chunk, start, fast.raw, fast.xterm, fast.c1.pend, slow.raw, slow.xterm, slow.c1.pend)
				}
				checkNotifyBuffers(t, &fast)
				checkNotifyBuffers(t, &slow)
			}
		}
	}
	// Exercise every parser state, including valid divergence between readings.
	for raw := range vtStates {
		for x := range vtStates {
			for _, stream := range streams {
				fast := outStream{raw: raw, xterm: x}
				slow := outStream{raw: raw, xterm: x}
				fast.feed(stream)
				for _, c := range stream {
					slow.step(c)
				}
				if !sameNotifyState(&fast, &slow) {
					t.Fatalf("initial(%d,%d), stream%x differs", raw, x, stream)
				}
			}
		}
	}
}

func TestNotificationRequiresEverySafeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		raw, x       bool
		pending      bool
	}{
		{"raw OSC still open", "\x1b]52;c;QUFB\x9c", false, true, false},
		{"raw reading sees C1 inside escape", "\x1b❯52;c;QUFB", false, true, false},
		{"xterm raw C1 OSC", "\x9d52;c;QUFB", true, false, false},
		{"xterm UTF8 C1 OSC", "\xc2\x9d52;c;QUFB", true, false, false},
		{"UTF8 lead", "\xe2", true, true, true}, {"UTF8 continuation", "\xe2\x9d", true, true, true}, {"C1 UTF8 lead", "\xc2", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s outStream
			s.feed([]byte(tc.prefix))
			if (s.raw == vtGround) != tc.raw || (s.xterm == vtGround) != tc.x || (len(s.c1.pend) > 0) != tc.pending {
				t.Fatalf("witness state=(%d,%d,%x)", s.raw, s.xterm, s.c1.pend)
			}
			if s.ground() {
				t.Fatal("unsafe reading was ignored")
			}
		})
	}
}

func TestUnicodeTextDoesNotHideNotifications(t *testing.T) {
	for _, text := range []string{"❯", "Ü", "✔", "😀"} {
		for cut := 0; cut <= len(text); cut++ {
			var s outStream
			s.feed([]byte(text[:cut]))
			s.feed([]byte(text[cut:]))
			if !s.ground() {
				t.Fatalf("Unicode %q split%d became a control: %d %d %x", text, cut, s.raw, s.xterm, s.c1.pend)
			}
		}
	}
}

func TestMixedLargeFramesDoNotFillDecoderBuffers(t *testing.T) {
	data := []byte("\xc2\x9d52;c;" + strings.Repeat("A", 128<<10) + "\xc2\x9c\x1b\\")
	var s outStream
	s.feed(data)
	if !s.ground() {
		t.Fatal("complete mixed C1 frame remains open")
	}
	checkNotifyBuffers(t, &s)
}

func TestNotificationsPreserveC1AndUnicodeAtEverySplit(t *testing.T) {
	var streams [][]byte
	for _, intro := range []byte{0x9d, 0x9b, 0x90, 0x98, 0x9e, 0x9f} {
		for _, encoded := range []bool{false, true} {
			start, end := []byte{intro}, []byte{0x9c}
			if encoded {
				start = []byte{0xc2, intro}
				end = []byte{0xc2, 0x9c}
			}
			body := []byte("payload ❯ Ü ✔ 😀")
			if intro == 0x9d {
				body = []byte("52;c;QUFB")
			}
			if intro == 0x9b {
				body = []byte("2J")
			}
			s := append([]byte("stale"), start...)
			s = append(s, body...)
			s = append(s, end...)
			s = append(s, []byte("\x1b\\OK")...)
			streams = append(streams, s)
		}
	}
	for _, text := range []string{"❯", "Ü", "✔", "😀"} {
		for _, prefix := range []string{"", "\x1b]2;", "\x1bPq", "\x1b_"} {
			streams = append(streams, []byte("before"+prefix+text+"\x1b\\OK"))
		}
	}
	notice := []byte("\x1b]777;notify;" + brand.DisplayName + " · box;NOTICE\x1b\\")
	render := func(b []byte) string { v := vt.NewEmulator(40, 6); _, _ = v.Write(b); return v.String() }
	for i, stream := range streams {
		for cut := 0; cut <= len(stream); cut++ {
			f, err := os.CreateTemp(t.TempDir(), "out")
			if err != nil {
				t.Fatal(err)
			}
			a := &attacher{o: AttachOptions{Out: f}}
			a.writeOutput(t.Context(), stream[:cut])
			a.emitNotify(t.Context(), notice)
			a.writeOutput(t.Context(), stream[cut:])
			got, err := os.ReadFile(f.Name())
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(got, notice) {
				t.Fatalf("stream%d cut%d lost notice state=(%d,%d,%x)", i, cut, a.outStream.raw, a.outStream.xterm, a.outStream.c1.pend)
			}
			if restored := bytes.ReplaceAll(got, notice, nil); !bytes.Equal(restored, stream) {
				t.Fatalf("stream%d cut%d changed session bytes", i, cut)
			}
			if g, w := render(got), render(stream); g != w {
				t.Fatalf("stream%d cut%d screen=%q want%q", i, cut, g, w)
			}
			checkNotifyBuffers(t, &a.outStream)
		}
	}
}

func TestLongControlStringsKeepBoundedNotificationQueue(t *testing.T) {
	for _, intro := range []string{"\x1b]52;c;", "\x1bPq", "\x1b_"} {
		f, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatal(err)
		}
		a := &attacher{o: AttachOptions{Out: f}}
		data := []byte(intro + strings.Repeat("A", 128<<10))
		a.writeOutput(t.Context(), data)
		var notices [][]byte
		for i := range maxPendingNotify + 3 {
			seq := []byte(fmt.Sprintf("\x1b]777;notify;title;notice%d\x1b\\", i))
			a.emitNotify(t.Context(), seq)
			notices = append(notices, seq)
		}
		if len(a.pendingNotify) != maxPendingNotify {
			t.Fatalf("queue grew to%d", len(a.pendingNotify))
		}
		mid, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mid, data) {
			t.Fatal("unterminated control string was changed")
		}
		a.writeOutput(t.Context(), []byte("\x1b\\OK"))
		got, err := os.ReadFile(f.Name())
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		last := -1
		withNotices := got
		for i, seq := range notices {
			at := bytes.Index(withNotices, seq)
			if i < 3 {
				if at >= 0 {
					t.Fatal("dropped oldest notice reappeared")
				}
				continue
			}
			if at <= last {
				t.Fatal("notification FIFO changed")
			}
			last = at
			got = bytes.ReplaceAll(got, seq, nil)
		}
		if !bytes.Equal(got, append(data, []byte("\x1b\\OK")...)) {
			t.Fatal("queued notifications changed control string bytes")
		}
		checkNotifyBuffers(t, &a.outStream)
	}
}

func FuzzNotificationFastState(f *testing.F) {
	for _, seed := range notificationStreams() {
		f.Add(seed, uint8(3))
	}
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		chunk := int(step)%31 + 1
		var fast, slow outStream
		for start := 0; start < len(data); start += chunk {
			part := data[start:min(len(data), start+chunk)]
			fast.feed(part)
			for _, c := range part {
				slow.step(c)
			}
			if !sameNotifyState(&fast, &slow) {
				t.Fatal("fast/byte state differs")
			}
			checkNotifyBuffers(t, &fast)
		}
	})
}
