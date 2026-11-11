package link

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// fuzzOptions are small, so that the fuzzer reaches every limit.
var fuzzOptions = Options{Window: 64, MaxGuestStreams: 4, MaxSupervisorStreams: 2, MaxHeader: 64}

// frameBytes writes stream frames as the other end would send them.
func frameBytes(frames ...func(w *proto.Writer)) []byte {
	var b bytes.Buffer
	w := proto.NewWriter(&b)
	for _, f := range frames {
		f(w)
	}
	return b.Bytes()
}

func fOpen(id uint32, hdr string) func(*proto.Writer) {
	return func(w *proto.Writer) { _ = w.Write(proto.TStreamOpen, append(idPayload(id), hdr...)) }
}

func fData(id uint32, n int) func(*proto.Writer) {
	return func(w *proto.Writer) { _ = w.Write(proto.TStreamData, append(idPayload(id), make([]byte, n)...)) }
}

func fClose(id uint32, code byte) func(*proto.Writer) {
	return func(w *proto.Writer) { _ = w.Write(proto.TStreamClose, idPayload(id, code)) }
}

func fWindow(id, n uint32) func(*proto.Writer) {
	return func(w *proto.Writer) {
		_ = w.Write(proto.TStreamWindow, binary.LittleEndian.AppendUint32(idPayload(id), n))
	}
}

// The link's frame reader parses a hostile peer's hello and frames on the
// trusted side. The harness feeds the frames one at a time. Between them,
// it reads and writes each stream as the bytes of acts say, and runs the
// writer, so the receiver gives credit and the sender spends it. Whatever
// the other end sends:
//   - nothing panics, and the input ends in an error;
//   - each stream's window adds up: the credit sent, the bytes held, the
//     bytes read and the credit queued come to Window, so the other end can
//     never make it hold more;
//   - each stream's buffers hold at most a window of memory each way;
//   - each side keeps to its stream limit, and the queues stay within the
//     streams.
func FuzzReadFrames(f *testing.F) {
	right := mustJSON(helloOf(Guest))
	sup := mustJSON(helloOf(Supervisor))
	f.Add(false, right, frameBytes(fOpen(2, `{"kind":"egress","host":"a","port":443}`), fData(2, 10), fClose(2, closeWrite)), []byte{40, 1})
	f.Add(false, right, frameBytes(fOpen(2, `{"kind":"dns"}`), fData(2, 64), fData(2, 1)), []byte{64})
	f.Add(false, right, frameBytes(fOpen(2, `{"kind":"egress"}`), fData(2, 64), fData(2, 40), fData(2, 1)), []byte{33, 33, 33})
	f.Add(false, right, frameBytes(fOpen(2, `{"kind":"holder"}`), fOpen(4, `{}`), fOpen(6, `[`), fData(4, 1)), []byte{1})
	f.Add(false, right, frameBytes(fOpen(2, `{"kind":"egress"}`), fOpen(4, `{"kind":"egress"}`), fOpen(6, `{"kind":"egress"}`),
		fOpen(8, `{"kind":"egress"}`), fOpen(10, `{"kind":"egress"}`), fClose(2, closeBoth), fData(2, 1)), []byte{3})
	f.Add(false, right, frameBytes(fWindow(1, 1), fClose(3, closeRefused), fData(2, 0)), []byte{})
	f.Add(true, sup, frameBytes(fOpen(1, `{"kind":"ctl"}`), fOpen(3, `{"kind":"ctl"}`), fWindow(1, math.MaxUint32)), []byte{0x81})
	f.Add(true, sup, frameBytes(fOpen(1, `{"kind":"put","size":3}`), fData(1, 3), fClose(1, closeWrite), fClose(1, closeBoth)), []byte{2})
	f.Add(true, sup, frameBytes(fOpen(1, `{"kind":"exec"}`), fWindow(1, 64), fData(1, 64)), []byte{0xff, 0x81, 0x81})
	f.Add(true, []byte(`{"magic":"x"}`), []byte{0, 0, 0, 0}, []byte{})
	f.Add(false, right, []byte{0xff, 0xff, 0xff, 0xff, 0x51}, []byte{})
	f.Fuzz(func(t *testing.T, guest bool, hello, in, acts []byte) {
		side := Supervisor
		if guest {
			side = Guest
		}
		s := newSession(nil, side, fuzzOptions)
		r := bufio.NewReader(bytes.NewReader(append(frameBytes(func(w *proto.Writer) { _ = w.Write(proto.THello, hello) }), in...)))
		err := s.readHello(r)
		for i := 0; err == nil; i++ {
			var fr proto.Frame
			if fr, err = s.readFrame(r); err == nil {
				err = s.handle(fr)
			}
			act(s, acts, i)
			_ = s.batch()
			check(t, s)
		}
		var ve *VersionError
		if !errors.Is(err, ErrBreach) && !errors.Is(err, ErrLinkClosed) && !errors.As(err, &ve) {
			t.Fatalf("the input ended with %v", err)
		}
	})
}

// act reads, writes or half-closes each stream, as the bytes of acts say.
// None of them waits: a read only reads bytes that are there, and a write
// fits in the room that the stream has.
func act(s *Session, acts []byte, i int) {
	if len(acts) == 0 {
		return
	}
	ids := make([]uint32, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for j, id := range ids {
		a := acts[(i+j)%len(acts)]
		st := s.streams[id]
		switch {
		case a == 0xff:
			_ = st.CloseWrite()
		case a&0x80 != 0:
			room := int(s.opt.Window) - st.out.len()
			_, _ = st.Write(make([]byte, min(room, int(a&0x7f))))
		case st.in.len() > 0:
			_, _ = st.Read(make([]byte, int(a)+1))
		}
	}
}

// check holds the bounds that a hostile peer must never pass.
func check(t *testing.T, s *Session) {
	t.Helper()
	w := int64(s.opt.Window)
	for id, st := range s.streams {
		if sum := st.recvWin + int64(st.in.len()) + st.consumed + st.grant; sum != w {
			t.Fatalf("stream %d: credit %d, held %d, read %d and queued credit %d add up to %d, not %d",
				id, st.recvWin, st.in.len(), st.consumed, st.grant, sum, w)
		}
		if st.in.cap() > int(w) || st.out.cap() > int(w) || st.sendCred > math.MaxUint32 {
			t.Fatalf("stream %d: buffers of %d and %d bytes, credit %d", id, st.in.cap(), st.out.cap(), st.sendCred)
		}
	}
	for _, d := range []Side{Supervisor, Guest} {
		if s.count[d] > s.opt.maxOpen(d) {
			t.Fatalf("%d streams of the %s", s.count[d], d)
		}
	}
	if n := len(s.streams); len(s.hi)+len(s.lo) > n || len(s.grants) > n || len(s.accepts) > n || len(s.refusals) != 0 {
		t.Fatalf("queues of %d, %d, %d and %d for %d streams", len(s.hi)+len(s.lo), len(s.grants), len(s.accepts), len(s.refusals), n)
	}
}
