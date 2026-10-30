package host

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests run the Go side of the shadow terminal against a fake child,
// a shell script, so they need no Zig. The fake writes the frames it is
// given first, then echoes every frame it reads, so each request is answered
// with its own payload, and keeps a copy of its input in a log.

// fakeVT writes the fake child and returns its path and its input log.
func fakeVT(t *testing.T, first ...[]byte) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	path, log = filepath.Join(dir, "fake-vt"), filepath.Join(dir, "input")
	var pre strings.Builder
	for _, f := range first {
		for _, b := range f {
			fmt.Fprintf(&pre, "\\%03o", b)
		}
	}
	script := "#!/bin/sh\necho \"fake shadow terminal $*\" >&2\nprintf '" + pre.String() + "'\nexec tee -a " + log + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

// frames decodes a log of frames.
func frames(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	for len(b) > 0 {
		if len(b) < 5 {
			t.Fatalf("a torn frame header: %q", b)
		}
		n := int(binary.LittleEndian.Uint32(b[1:5]))
		if len(b) < 5+n {
			t.Fatalf("a torn frame: %q", b)
		}
		out = append(out, fmt.Sprintf("%c%q", b[0], b[5:5+n]))
		b = b[5+n:]
	}
	return out
}

// childFrames waits until the child's input log holds n frames.
func childFrames(t *testing.T, log string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(log)
		if f := frames(t, b); len(f) >= n || time.Now().After(deadline) {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVTFramesWithAFakeChild(t *testing.T) {
	bell := vtFrame(vtFrameEvent, []byte(`{"type":"bell","value":-1}`))
	reply := vtFrame(vtFrameWrite, []byte("\x1b[?62;22c"))
	path, log := fakeVT(t, bell, reply)
	var mu sync.Mutex
	var events []VTEvent
	var writes []string
	var logs lineLog
	v, err := StartVT(VTOptions{
		Path: path, Cols: 100, Rows: 30, Scrollback: 500,
		OnEvent: func(e VTEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
		OnWrite: func(p []byte) { mu.Lock(); writes = append(writes, string(p)); mu.Unlock() },
		Log:     logs.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	v.Feed(0, []byte("hello"))
	v.Feed(5, nil)
	v.Resize(120, 40, 960, 640)
	v.Resize(0, 40, 0, 0) // ignored
	v.AnswerQueries(true)
	v.SetProfile(VTProfile{XTVersion: "test 1.0"})
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	snap, upTo, err := v.Snapshot(90, 20, 50, 5*time.Second)
	if err != nil || upTo != 5 {
		t.Fatalf("snapshot = %q, %d, %v", snap, upTo, err)
	}
	// The fake echoes the request: cols, rows and the scrollback lines.
	if want := []byte{90, 0, 20, 0, 50, 0, 0, 0}; !bytes.Equal(snap, want) {
		t.Errorf("snapshot request = %v, want %v", snap, want)
	}
	if _, _, err := v.Snapshot(0, 20, 50, time.Second); err == nil {
		t.Error("a snapshot of no size worked")
	}
	if d, err := v.dump(7, 5*time.Second); err != nil || !bytes.Equal(d, []byte{7, 0, 0, 0}) {
		t.Errorf("dump = %v, %v", d, err)
	}

	want := []string{
		`R"d\x00\x1e\x00\x00\x00\x00\x00"`, // 100 by 30, first
		`Q"\x00"`,                          // no answers yet
		`D"hello"`,
		`R"x\x00(\x00\xc0\x03\x80\x02"`, // 120 by 40, 960 by 640 pixels
		`Q"\x01"`,
		`P"{\"xtversion\":\"test 1.0\"}"`,
		`Y""`,
		`S"Z\x00\x14\x002\x00\x00\x00"`,
		`T"\a\x00\x00\x00"`,
	}
	if got := childFrames(t, log, len(want)); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the child got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Type != "bell" || len(writes) != 1 || writes[0] != "\x1b[?62;22c" {
		t.Errorf("events %+v, writes %q", events, writes)
	}
	if lines := logs.get(); len(lines) == 0 || !strings.Contains(lines[0], `msg="shadow terminal stderr" line="fake shadow terminal --cols 100 --rows 30 --scrollback 500"`) {
		t.Errorf("logs = %q", lines)
	}
}

// A screen for a preview is a snapshot at the size the shadow terminal has,
// with no scrollback, so the child does not resize, and neither does the PTY.
func TestVTScreenKeepsTheSize(t *testing.T) {
	path, log := fakeVT(t)
	v, err := StartVT(VTOptions{Path: path, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	v.Feed(0, []byte("hello"))
	v.Resize(120, 40, 960, 640)
	snap, upTo, err := v.Screen(5 * time.Second)
	if err != nil || upTo != 5 {
		t.Fatalf("screen = %q, %d, %v", snap, upTo, err)
	}
	// The fake echoes the request: the size it has, and no scrollback.
	if want := []byte{120, 0, 40, 0, 0, 0, 0, 0}; !bytes.Equal(snap, want) {
		t.Errorf("screen request = %v, want %v", snap, want)
	}
	want := []string{
		`R"d\x00\x1e\x00\x00\x00\x00\x00"`,
		`Q"\x00"`,
		`D"hello"`,
		`R"x\x00(\x00\xc0\x03\x80\x02"`,
		`S"x\x00(\x00\x00\x00\x00\x00"`, // 120 by 40, as it is
	}
	if got := childFrames(t, log, len(want)); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the child got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cols != 120 || v.rows != 40 || v.xpx != 960 || v.ypx != 640 {
		t.Errorf("after a screen, the size is %dx%d, %dx%d px", v.cols, v.rows, v.xpx, v.ypx)
	}
}

func TestVTRestartsWithBackoff(t *testing.T) {
	old := vtBackoff
	vtBackoff = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	t.Cleanup(func() { vtBackoff = old })
	path, log := fakeVT(t)
	var mu sync.Mutex
	hist := []byte("history")
	var logs lineLog
	v, err := StartVT(VTOptions{
		Path:    path,
		History: func() ([]byte, int64) { mu.Lock(); defer mu.Unlock(); return bytes.Clone(hist), 10 },
		Log:     logs.logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	pid := func() int {
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.child == nil {
			return 0
		}
		return v.child.cmd.Process.Pid
	}
	first := pid()
	// tee may echo a frame, which ends sync, before it appends it to the
	// log: wait for the first child's three frames before it is killed.
	childFrames(t, log, 3)
	// The child crashes; output that arrives meanwhile is not lost.
	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	hist = append(hist, " more"...)
	mu.Unlock()
	v.Feed(17, []byte(" more"))
	deadline := time.Now().Add(5 * time.Second)
	for p := pid(); p == 0 || p == first; p = pid() {
		if time.Now().After(deadline) {
			t.Fatal("the shadow terminal was not restarted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := v.sync(); err != nil {
		t.Fatal(err)
	}
	if !logs.has(`msg="shadow terminal stopped, restarting it"`, "wait=10ms") {
		t.Errorf("logs = %q", logs.get())
	}
	got := strings.Join(childFrames(t, log, 8), " ")
	if strings.Count(got, `D"history"`) != 1 || !strings.Contains(got, `D"history more"`) {
		t.Errorf("the new child was not fed the history: %s", got)
	}
	// It cannot start again: each try waits longer, until it is closed.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(pid(), syscall.SIGKILL)
	waitUntil(t, func() bool { return logs.has(`msg="shadow terminal did not start, trying again"`, "wait=40ms") })
	if _, _, err := v.Snapshot(80, 24, 0, time.Second); err == nil {
		t.Error("a snapshot worked with no shadow terminal")
	}
	v.Feed(22, []byte("x")) // dropped, without a child
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v.Feed(23, []byte("y"))
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestVTLogWriter(t *testing.T) {
	var logs lineLog
	w := &vtLogWriter{log: logs.logger()}
	_, _ = w.Write([]byte("one\ntw"))
	_, _ = w.Write([]byte("o\n"))
	_, _ = w.Write(bytes.Repeat([]byte("x"), 5000))
	got := logs.get()
	if len(got) != 3 || !strings.HasSuffix(got[0], `msg="shadow terminal stderr" line=one`) ||
		!strings.HasSuffix(got[1], " line=two") || !strings.HasSuffix(got[2], " line="+strings.Repeat("x", 5000)) {
		t.Errorf("logged %d lines: %.80q", len(got), got)
	}
}

// lineLog keeps the lines that a logger writes, for tests.
type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

// logger returns a logger that writes its lines to l, as the holder's does.
func (l *lineLog) logger() *slog.Logger { return newHolderLog(l, nil) }

func (l *lineLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// has reports whether one line holds all the parts.
func (l *lineLog) has(parts ...string) bool {
	for _, line := range l.get() {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(line, p)
		}
		if all {
			return true
		}
	}
	return false
}
