package supervisor

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cl "github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func writeHeaderOnly(t *testing.T, st *link.Stream, typ proto.Type, payloadLen int) {
	t.Helper()
	var hdr [5]byte
	binary.LittleEndian.PutUint32(hdr[:4], uint32(payloadLen+1))
	hdr[4] = byte(typ)
	if _, err := st.Write(hdr[:]); err != nil {
		t.Fatalf("write %v header: %v", typ, err)
	}
}

//go:noinline
func streamMaxOutputOnce(t *testing.T, st *link.Stream, r *bufio.Reader) {
	t.Helper()
	const payloadLen = proto.MaxFrame - 1
	done := make(chan error, 1)
	go func() {
		var hdr [5]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			done <- err
			return
		}
		if proto.Type(hdr[4]) != proto.TOutput {
			done <- errors.New("expected TOutput")
			return
		}
		rem := int(binary.LittleEndian.Uint32(hdr[:4])) - 1
		sink := make([]byte, 32<<10)
		for rem > 0 {
			n, err := r.Read(sink[:min(len(sink), rem)])
			rem -= n
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	writeHeaderOnly(t, st, proto.TOutput, payloadLen)
	chunk := make([]byte, 32<<10)
	for rem := payloadLen; rem > 0; {
		step := min(len(chunk), rem)
		if _, err := st.Write(chunk[:step]); err != nil {
			t.Fatalf("write TOutput chunk: %v", err)
		}
		rem -= step
	}
	if err := <-done; err != nil {
		t.Fatalf("read TOutput: %v", err)
	}
}

// Oversized error headers are refused before allocation, concurrent 4 MiB
// output frames across 16 and 31 streams stay within the supervisor's relay
// budget, and an idle stream in Peek drops its 4 MiB buffer immediately.
func TestGuestHeaderAllocationsStayBounded(t *testing.T) {
	// Part 1: 16 and 31 concurrent 4 MiB TError headers are refused before
	// allocating their bodies, with guest_frame and near-zero heap growth.
	for _, nStreams := range []int{16, 31} {
		func() {
			h := hostileStart(t)
			h.answerStart(t)
			conns := make([]net.Conn, nStreams)
			readers := make([]*bufio.Reader, nStreams)
			streams := make([]*link.Stream, nStreams)
			for i := range nStreams {
				conns[i], readers[i], streams[i] = attachTo(t, h)
				defer conns[i].Close()
				defer streams[i].Close()
			}
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)

			for _, st := range streams {
				writeHeaderOnly(t, st, proto.TError, proto.MaxFrame-1)
			}
			for i, r := range readers {
				f, err := proto.Read(r)
				var pe proto.Error
				if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Code != "guest_frame" {
					t.Fatalf("%d streams, stream %d: got %v %s, %v", nStreams, i, f.Type, f.Payload, err)
				}
				if _, err := proto.Read(r); !errors.Is(err, io.EOF) {
					t.Fatalf("%d streams, stream %d stayed open: %v", nStreams, i, err)
				}
			}
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > 2<<20 {
				t.Fatalf("%d 4 MiB TError headers grew heap by %d bytes", nStreams, growth)
			}
			_, _ = host.HolderRequest(h.id, "kill", nil, time.Second)
			_ = h.waitEnd(t, 15*time.Second)
		}()
	}

	// Part 2: 16 and 31 concurrent 4 MiB TOutput headers and stalled bodies
	// stay bounded below 16 MiB live heap growth.
	for _, nStreams := range []int{16, 31} {
		func() {
			oldStall := frameStall
			frameStall = 400 * time.Millisecond
			defer func() { frameStall = oldStall }()

			h := hostileStart(t)
			h.answerStart(t)
			conns := make([]net.Conn, nStreams)
			readers := make([]*bufio.Reader, nStreams)
			streams := make([]*link.Stream, nStreams)
			for i := range nStreams {
				conns[i], readers[i], streams[i] = attachTo(t, h)
				defer conns[i].Close()
				defer streams[i].Close()
			}
			runtime.GC()
			runtime.GC()
			var before, peak runtime.MemStats
			runtime.ReadMemStats(&before)

			for _, st := range streams {
				writeHeaderOnly(t, st, proto.TOutput, proto.MaxFrame-1)
				_, _ = st.Write([]byte("0123456789"))
			}
			// Let every relay goroutine read its header and enter relayBody.
			time.Sleep(120 * time.Millisecond)
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&peak)
			if growth := int64(peak.HeapAlloc) - int64(before.HeapAlloc); growth > 16<<20 {
				t.Fatalf("%d concurrent 4 MiB TOutput headers grew live heap by %d MiB (limit 16 MiB)", nStreams, growth>>20)
			}

			var wg sync.WaitGroup
			for i := range nStreams {
				wg.Go(func() {
					_ = conns[i].Close()
					_ = streams[i].Close()
				})
			}
			wg.Wait()
			_, _ = host.HolderRequest(h.id, "kill", nil, time.Second)
			_ = h.waitEnd(t, 15*time.Second)
		}()
	}

	// Part 3: after a full 4 MiB legal TOutput passes and the stream returns
	// to Peek(1), GC reclaims the 4 MiB buffer immediately.
	h := hostileStart(t)
	h.answerStart(t)
	conn, r, st := attachTo(t, h)
	defer conn.Close()
	defer st.Close()

	// Warm up the small reusable buffer and stream state.
	if err := proto.NewWriter(st).Write(proto.TOutput, proto.EncodeOutput(0, []byte("warm"))); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Read(r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	runtime.GC()
	runtime.GC()
	var beforeIdle, afterIdle runtime.MemStats
	runtime.ReadMemStats(&beforeIdle)

	streamMaxOutputOnce(t, st, r)
	// Wait until relay loops back into r.Peek(1) while conn and st stay open.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&afterIdle)
	if retained := int64(afterIdle.HeapAlloc) - int64(beforeIdle.HeapAlloc); retained > 1<<20 {
		t.Fatalf("idle stream in Peek retained %d bytes after 4 MiB TOutput", retained)
	}
}

// A hostile guest holder's error answers to ports, peek and send never pass
// forged codes, hints or data: both host.HolderRequest and the real client
// receive guest_error, a cleaned message attributed to the sandbox, nil Data,
// and the trusted hint.
func TestAGuestResponseErrorIsTheGuests(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)

	const trustedHint = "Check the session in the sandbox, or resume it."
	answerHostile := func() {
		st := <-h.g.holders
		defer st.Close()
		gr, gw := bufio.NewReader(st), proto.NewWriter(st)
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
		reqFrame, err := proto.Read(gr)
		if err != nil {
			return
		}
		var req proto.Request
		_ = json.Unmarshal(reqFrame.Payload, &req)
		_ = gw.WriteJSON(proto.TResponse, proto.Response{
			ID:     req.ID,
			OK:     false,
			Result: json.RawMessage(`{"sneaky":true}`),
			Error: &proto.Error{
				Code:    "not_found",
				Message: "lie\x1b]52;c;aGk=\x07",
				Hint:    "evil command",
				Data:    json.RawMessage(`{"forged":1}`),
			},
		})
	}

	for _, op := range []string{"ports", "peek", "send"} {
		go answerHostile()
		var args any
		if op == "send" {
			args = map[string]any{"text": "hi"}
		}
		raw, err := host.HolderRequest(h.id, op, args, 3*time.Second)
		if err == nil || raw != nil {
			t.Fatalf("HolderRequest(%s) = %s, nil; want guest_error", op, raw)
		}
		var pe *proto.Error
		if !errors.As(err, &pe) {
			t.Fatalf("HolderRequest(%s) error = %T %v, want *proto.Error", op, err, err)
		}
		if pe.Code != "guest_error" || !strings.HasPrefix(pe.Message, "the sandbox says: ") ||
			strings.ContainsAny(pe.Message, "\x1b\x07") || pe.Data != nil || pe.Hint != trustedHint {
			t.Fatalf("HolderRequest(%s) got %+v", op, pe)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := cl.Dial(ctx, bridgeHost(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	calls := []struct {
		name string
		run  func() error
	}{
		{"ports", func() error { _, err := conn.Ports(ctx, h.id); return err }},
		{"peek", func() error { _, _, _, err := conn.Peek(ctx, h.id, 1024); return err }},
		{"send", func() error { return conn.SendText(ctx, h.id, "hi", true) }},
	}
	for _, tc := range calls {
		go answerHostile()
		err := tc.run()
		var ue *cl.UserError
		if !errors.As(err, &ue) {
			t.Fatalf("client %s error = %T %v, want *client.UserError", tc.name, err, err)
		}
		if ue.Code != "guest_error" || !strings.HasPrefix(ue.Msg, "the sandbox says: ") ||
			strings.ContainsAny(ue.Msg, "\x1b\x07") || ue.Data != nil || ue.Hint != trustedHint {
			t.Fatalf("client %s got %+v", tc.name, ue)
		}
	}
}
