package link

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// frameOf is a frame as a test compares it: its type, its stream and the
// bytes after the stream ID.
type frameOf struct {
	t    proto.Type
	id   uint32
	size int
}

func (f frameOf) String() string { return fmt.Sprintf("%#x/%d/%d", byte(f.t), f.id, f.size) }

// decode reads the frames of a batch.
func decode(tb testing.TB, b []byte) []frameOf {
	tb.Helper()
	r := bufio.NewReader(bytes.NewReader(b))
	var out []frameOf
	for {
		f, err := proto.Read(r)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, frameOf{f.Type, binary.LittleEndian.Uint32(f.Payload), len(f.Payload) - 4})
	}
}

// batch builds the next batch, as the writer does, and counts it as
// written.
func (s *Session) batch() []byte {
	b, seq := s.build()
	if len(b) > 0 {
		s.wrote(seq)
	}
	return b
}

// queue puts n bytes in st's send buffer and schedules it. Under s.mu, or
// with no other goroutine.
func queue(st *Stream, n int) {
	st.out.write(make([]byte, n), int(st.s.opt.Window))
	st.s.schedule(st)
}

// The writer sends opens in the order of their IDs, then credit and
// refusals, then ctl and holder streams first, and the other streams in
// turn, one frame each. A close goes after the stream's last bytes.
func TestBatchOrder(t *testing.T) {
	s := newSession(nil, Supervisor, Options{Window: 128 << 10})
	waiting := func(id uint32, k Kind, n int) *Stream {
		st := s.newStream(id, k, nil)
		s.add(st)
		queue(st, n)
		return st
	}
	waiting(1, KindPort, 100<<10)
	put := waiting(3, KindPut, 100<<10)
	put.closeCode = closeWrite
	waiting(5, KindHolder, 100<<10)
	for _, id := range []uint32{7, 9} {
		st := s.newStream(id, KindExec, []byte(`{"kind":"exec"}`))
		s.add(st)
		s.opens = append(s.opens, st)
	}
	s.nextID, s.nextPeer = 11, 4
	in := s.newStream(2, KindEgress, nil)
	s.add(in)
	in.recvWin, in.consumed = 0, 10
	s.giveCredit(in)
	s.refusals = append(s.refusals, 4)
	var got []string
	for range 3 {
		var batch []string
		for _, f := range decode(t, s.batch()) {
			batch = append(batch, f.String())
		}
		got = append(got, fmt.Sprint(batch))
	}
	want := []string{
		"[0x50/7/15 0x50/9/15 0x53/2/4 0x52/4/1 0x51/5/65531 0x51/5/36869 0x51/1/65531 0x51/3/65531 0x51/1/36869]",
		"[0x51/3/36869 0x52/3/1]",
		"[]",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("batches\n%s\nwant\n%s", got, want)
	}
	if put.queued || len(s.lo) != 0 || len(s.hi) != 0 || len(s.grants) != 0 {
		t.Errorf("still queued: %d, %d and %d", len(s.lo), len(s.hi), len(s.grants))
	}
	if s.sentID != 9 || in.recvWin != 10 {
		t.Errorf("last open sent %d, credit %d", s.sentID, in.recvWin)
	}
}

// A stream with no credit sends nothing, and its close waits for its
// bytes. Credit lets them go.
func TestBatchWaitsForCredit(t *testing.T) {
	s := newSession(nil, Supervisor, Options{Window: 8})
	st := s.newStream(1, KindPort, nil)
	s.add(st)
	s.nextID, s.sentID = 3, 1
	queue(st, 8)
	if got := fmt.Sprint(decode(t, s.batch())); got != "[0x51/1/8]" {
		t.Fatalf("first batch %s", got)
	}
	queue(st, 4)
	st.closeCode = closeBoth
	if got := fmt.Sprint(decode(t, s.batch())); got != "[]" {
		t.Fatalf("a batch without credit %s", got)
	}
	if err := s.onWindow(binary.LittleEndian.AppendUint32(idPayload(1), 8)); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(decode(t, s.batch())); got != "[0x51/1/4 0x52/1/1]" {
		t.Fatalf("after credit %s", got)
	}
	if !st.gone || len(s.streams) != 0 || s.count[Supervisor] != 0 {
		t.Error("the stream is still there after its close")
	}
}

// Credit counts only once its frame goes to the writer. Bytes that the
// other end sends on credit that is still queued break the link.
func TestCreditCountsWhenItsFrameGoes(t *testing.T) {
	data := func(s *Session, n int) error {
		return s.onData(append(idPayload(1), make([]byte, n)...))
	}
	for _, sent := range []bool{false, true} {
		s := newSession(nil, Guest, Options{Window: 8})
		st := s.newStream(1, KindHolder, nil)
		s.add(st)
		s.nextPeer = 3
		if err := data(s, 8); err != nil {
			t.Fatal(err)
		}
		if n, err := st.Read(make([]byte, 4)); n != 4 || err != nil {
			t.Fatalf("read %d, %v", n, err)
		}
		if st.grant != 4 || st.recvWin != 0 {
			t.Fatalf("credit queued %d, sent %d", st.grant, st.recvWin)
		}
		if sent {
			if got := fmt.Sprint(decode(t, s.batch())); got != "[0x53/1/4]" {
				t.Fatalf("batch %s", got)
			}
			if err := data(s, 4); err != nil {
				t.Fatalf("data on credit that went: %v", err)
			}
		}
		if err := data(s, 1); !errors.Is(err, ErrBreach) {
			t.Errorf("credit sent %v: data beyond it gave %v", sent, err)
		}
	}
}

// Reading gives credit back once half a window has been read, and not
// after the other end has closed its writes.
func TestCreditAfterReads(t *testing.T) {
	s := newSession(nil, Guest, Options{Window: 8})
	st := s.newStream(1, KindHolder, nil)
	s.add(st)
	s.nextPeer = 3
	data := func(b string) {
		if err := s.onData(append(idPayload(1), b...)); err != nil {
			t.Fatal(err)
		}
	}
	data("abcdef")
	buf := make([]byte, 3)
	if n, err := st.Read(buf); n != 3 || err != nil || len(s.grants) != 0 {
		t.Fatalf("read %d, %v, credit for %d streams", n, err, len(s.grants))
	}
	if n, _ := st.Read(buf[:1]); n != 1 || st.grant != 4 || st.recvWin != 2 {
		t.Fatalf("read %d, credit queued %d, credit %d", n, st.grant, st.recvWin)
	}
	_ = s.batch()
	data("ghijkl")
	if err := s.onClose(idPayload(1)); err == nil {
		t.Error("a short close passed")
	}
	if err := s.onClose(idPayload(1, closeWrite)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(st)
	if err != nil || string(got) != "efghijkl" || len(s.grants) != 0 {
		t.Fatalf("read %q, %v, credit for %d streams", got, err, len(s.grants))
	}
	if sum := st.recvWin + int64(st.in.len()) + st.consumed + st.grant; sum != 8 {
		t.Errorf("the window adds up to %d", sum)
	}
}

// The ring keeps every byte in order across wraps, and its memory never
// passes its limit, whatever the sizes of the writes.
func TestBuffer(t *testing.T) {
	const limit = 10 << 10
	rng := rand.New(rand.NewPCG(1, 2))
	var q buffer
	var want []byte
	next := byte(0)
	for range 5000 {
		if n := rng.IntN(3000); q.len()+n <= limit && rng.IntN(2) == 0 {
			p := make([]byte, n)
			for i := range p {
				p[i] = next
				next++
			}
			q.write(p, limit)
			want = append(want, p...)
		} else if q.len() > 0 {
			n := 1 + rng.IntN(q.len())
			var got []byte
			if rng.IntN(2) == 0 {
				got = make([]byte, n)
				got = got[:q.read(got)]
			} else {
				got = q.appendTo(nil, n)
			}
			if !bytes.Equal(got, want[:len(got)]) {
				t.Fatalf("read %d bytes that differ", len(got))
			}
			want = want[len(got):]
		}
		if q.cap() > limit || q.len() != len(want) {
			t.Fatalf("cap %d, len %d, want %d", q.cap(), q.len(), len(want))
		}
	}
	q.reset()
	if q.cap() != 0 || q.read(make([]byte, 1)) != 0 {
		t.Error("reset kept memory or bytes")
	}
	q.write(nil, limit)
	if q.cap() != 0 {
		t.Error("an empty write took memory")
	}
}
