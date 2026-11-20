package link

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// heap is the heap in use after a full collection.
func heap() int64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// waitLong waits up to 60 s for ok, for tests that move many frames.
func waitLong(tb testing.TB, ok func() bool) {
	tb.Helper()
	for range 6000 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatal("timed out")
}

// A guest that fills every stream's credit with 1-byte frames makes the
// supervisor hold no more than a window for each stream, heap counted.
func TestTinyFramesHoldAWindow(t *testing.T) {
	const n, window = 64, 4 << 10
	s, p := scripted(t, Supervisor, Options{Window: window, MaxGuestStreams: n}, helloOf(Guest))
	var opens bytes.Buffer
	w := proto.NewWriter(&opens)
	for i := range n {
		_ = w.Write(proto.TStreamOpen, append(idPayload(uint32(2+2*i)), `{"kind":"egress"}`...))
	}
	if _, err := p.conn.Write(opens.Bytes()); err != nil {
		t.Fatal(err)
	}
	waitLong(t, func() bool { return streams(s) == n })
	// The test's own frames are made before the heap is measured.
	var frames bytes.Buffer
	w = proto.NewWriter(&frames)
	for range window {
		for i := range n {
			_ = w.Write(proto.TStreamData, idPayload(uint32(2+2*i), 'x'))
		}
	}
	before := heap()
	go func() { _, _ = p.conn.Write(frames.Bytes()) }()
	waitLong(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, st := range s.streams {
			if st.in.len() < window {
				return false
			}
		}
		return true
	})
	grew := heap() - before
	runtime.KeepAlive(&frames)
	if limit := int64(n*(window+1<<10) + 1<<20); grew > limit {
		t.Errorf("%d streams of %d bytes of credit hold %d bytes of heap, over %d", n, window, grew, limit)
	}
	t.Logf("%d streams of %d bytes, filled with 1-byte frames: the heap grew by %d bytes", n, window, grew)
}

// A guest that never gives credit and never reads makes the supervisor
// queue no more than a window for each stream, heap counted, whatever the
// sizes of the writes.
func TestSendQueuesHoldAWindow(t *testing.T) {
	const window = 4 << 10
	s, _ := scripted(t, Supervisor, Options{Window: window}, helloOf(Guest))
	sts := make([]*Stream, DefaultMaxSupervisorStreams)
	for i := range sts {
		st, err := s.Open(testCtx(t), KindPort, nil)
		if err != nil {
			t.Fatal(err)
		}
		sts[i] = st
	}
	if _, err := s.Open(testCtx(t), KindPort, nil); err == nil || !strings.Contains(err.Error(), "the supervisor has 32 streams open") {
		t.Fatalf("a 33rd stream: %v", err)
	}
	before := heap()
	done := make(chan struct{}, len(sts))
	for _, st := range sts {
		go func() {
			defer func() { done <- struct{}{} }()
			one := []byte{'x'}
			for {
				if _, err := st.Write(one); err != nil {
					return
				}
			}
		}()
	}
	waitLong(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, st := range sts {
			if st.out.len() < window {
				return false
			}
		}
		return true
	})
	grew := heap() - before
	s.mu.Lock()
	queued := 0
	for _, st := range sts {
		queued += st.out.len()
	}
	s.mu.Unlock()
	if queued != len(sts)*window {
		t.Errorf("%d bytes queued, want %d", queued, len(sts)*window)
	}
	if limit := int64(len(sts)*(window+1<<10) + 1<<20); grew > limit {
		t.Errorf("%d streams of %d bytes queued hold %d bytes of heap, over %d", len(sts), window, grew, limit)
	}
	t.Logf("%d streams of %d bytes, filled with 1-byte writes: the heap grew by %d bytes", len(sts), window, grew)
	for _, st := range sts {
		_ = st.SetWriteDeadline(time.Now())
	}
	for range sts {
		<-done
	}
}

// The streams of each side count apart. A guest that holds all its
// streams cannot stop the supervisor from opening its own.
func TestStreamLimitsBySide(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	var opens bytes.Buffer
	w := proto.NewWriter(&opens)
	for i := range DefaultMaxGuestStreams + 1 {
		_ = w.Write(proto.TStreamOpen, append(idPayload(uint32(2+2*i)), `{"kind":"egress"}`...))
	}
	if _, err := p.conn.Write(opens.Bytes()); err != nil {
		t.Fatal(err)
	}
	last := uint32(2 + 2*DefaultMaxGuestStreams)
	if code := p.expectClose(last); code != closeRefused {
		t.Fatalf("the guest's stream past its limit closed with %d", code)
	}
	if s.Refused() != 1 || streams(s) != DefaultMaxGuestStreams {
		t.Fatalf("refused %d, %d streams", s.Refused(), streams(s))
	}
	kinds := []Kind{KindCtl, KindHolder, KindPort, KindExec, KindPut, KindBundle, KindGet, KindSFTP}
	for i := range DefaultMaxSupervisorStreams {
		if _, err := s.Open(testCtx(t), kinds[min(i, len(kinds)-1)], nil); err != nil {
			t.Fatalf("supervisor's stream %d: %v", i+1, err)
		}
	}
	if _, err := s.Open(testCtx(t), KindExec, nil); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("a stream past the supervisor's limit: %v", err)
	}
	if s.count[Guest] != DefaultMaxGuestStreams || s.count[Supervisor] != DefaultMaxSupervisorStreams {
		t.Errorf("counts %v", s.count)
	}
}

// A stream that leaves the link leaves nothing in the writer's queues, so
// streams that come and go while the writer is blocked never grow them.
// Here no writer runs at all.
func TestChurnLeavesNoQueue(t *testing.T) {
	s := newSession(nil, Supervisor, Options{Window: 64})
	for i := range 1000 {
		id := uint32(2 + 2*i)
		if err := s.onOpen(append(idPayload(id), `{"kind":"egress"}`...)); err != nil {
			t.Fatal(err)
		}
		if err := s.onData(append(idPayload(id), make([]byte, 40)...)); err != nil {
			t.Fatal(err)
		}
		st, err := s.Accept(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		if n, err := st.Read(make([]byte, 40)); n != 40 || err != nil {
			t.Fatalf("read %d, %v", n, err)
		}
		if _, err := st.Write(make([]byte, 10)); err != nil {
			t.Fatal(err)
		}
		if len(s.lo) != 1 || len(s.grants) != 1 {
			t.Fatalf("queues of %d and %d before the close", len(s.lo), len(s.grants))
		}
		if err := s.onClose(idPayload(id, closeBoth)); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if len(s.lo)+len(s.hi) != 0 || len(s.grants) != 0 || len(s.streams) != 0 || len(s.accepts) != 0 || s.count[Guest] != 0 {
			t.Fatalf("after %d streams: queues of %d, %d and %d, %d streams", i+1, len(s.lo)+len(s.hi), len(s.grants), len(s.accepts), len(s.streams))
		}
	}
}
