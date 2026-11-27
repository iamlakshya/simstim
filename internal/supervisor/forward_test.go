package supervisor

import (
	"bufio"
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
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// attachTo dials the session's socket, attaches, and plays the holder on
// the guest's side of the new holder stream: it answers the hello, and
// reads the attach. While the link has no free stream, it tries again,
// for up to 20 s. It returns the client's connection and the stream.
func attachTo(t *testing.T, h *hostileRun) (net.Conn, *bufio.Reader, *link.Stream) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var (
		conn net.Conn
		r    *bufio.Reader
		st   *link.Stream
	)
	for st == nil {
		if time.Now().After(deadline) {
			t.Fatal("no holder stream: the link has none free")
		}
		var err error
		conn, err = net.Dial("unix", paths.Socket(h.id))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		w := proto.NewWriter(conn)
		r = bufio.NewReader(conn)
		_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
		_, _ = proto.Read(r)
		_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
		select {
		case st = <-h.g.holders:
		case <-time.After(time.Second):
			conn.Close()
			time.Sleep(200 * time.Millisecond)
		}
	}
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	return conn, r, st
}

// A client that goes while the guest takes none of its input leaves
// nothing behind: its stream closes, its goroutines end, and more such
// clients than the link has streams still attach.
func TestAClientThatGoesFreesItsStream(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	base := runtime.NumGoroutine()
	input := make([]byte, 32<<10)
	var wg sync.WaitGroup
	for range 40 {
		conn, _, st := attachTo(t, h)
		w := proto.NewWriter(conn)
		// Six frames of 32 KiB: more than the stream's credit, which the
		// guest never gives back, and its buffer.
		for range 6 {
			_ = w.Write(proto.TInput, input)
		}
		conn.Close()
		// The guest reads nothing. It closes its end of the stream once
		// the supervisor has closed the stream, as a write of nothing
		// says, as the guest init does.
		wg.Go(func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := st.Write(nil); err != nil {
					_ = st.Close()
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
	wg.Wait()
	conn, _, _ := attachTo(t, h)
	conn.Close()
	waitFor(t, "the goroutines of the clients that went", func() bool {
		return runtime.NumGoroutine() <= base+8
	})
}

// A guest's error never reads as a stop of the session: its code becomes
// guest_error, and its message says that the sandbox said it.
func TestAGuestErrorIsTheGuests(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	_, r, st := attachTo(t, h)
	_ = proto.NewWriter(st).WriteJSON(proto.TError, proto.Error{Code: "session_gone", Message: "the session stopped\x1b]52;c;aGk=\x07"})
	f, err := proto.Read(r)
	var pe proto.Error
	if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil {
		t.Fatalf("the client got %v %s, %v", f.Type, f.Payload, err)
	}
	if pe.Code != "guest_error" || !strings.HasPrefix(pe.Message, "the sandbox says: ") || strings.ContainsAny(pe.Message, "\x1b\x07") || pe.Hint == "" {
		t.Errorf("a guest's error reached the client as %+v", pe)
	}
}

// A guest's end with an exit code that only the trusted side uses, or one
// outside 0 to 255, never reaches a client: the client hears that the
// guest sent a frame that may not pass.
func TestAReservedEndIsRefused(t *testing.T) {
	for _, code := range []int{-1, 256} {
		h := hostileStart(t)
		h.answerStart(t)
		_, r, st := attachTo(t, h)
		_ = proto.NewWriter(st).WriteJSON(proto.TEnded, proto.Ended{ExitCode: code})
		f, err := proto.Read(r)
		var pe proto.Error
		if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Code != "guest_frame" {
			t.Errorf("an end with code %d reached the client as %v %s, %v", code, f.Type, f.Payload, err)
		}
		if _, err := proto.Read(r); !errors.Is(err, io.EOF) {
			t.Errorf("the connection lasts after an end with code %d: %v", code, err)
		}
		_, _ = host.HolderRequest(h.id, "kill", nil, time.Second)
		_ = h.waitEnd(t, 15*time.Second)
	}
}

// The row of a session keeps the record's classification: a kill that a
// tick took for a child before the agent's claimed SIGKILL is not the
// agent's, in host.List and for the client too.
func TestTheRowKeepsAnOldChildKill(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	h.answerStart(t)
	set("memory.events", "max 3\noom 1\noom_kill 1")
	waitFor(t, "a child's kill", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.Reason == host.OOMChildReason
	})
	code := 137
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
	var row cl.Session
	waitFor(t, "the claimed end in the list", func() bool {
		list, err := host.List()
		for _, s := range list {
			if s.ID == h.id {
				row = s
			}
		}
		return err == nil && row.ExitCode != nil
	})
	if row.OOM || cl.CapStopped(row) || !cl.ChildStopped(row) || strings.Contains(cl.SessionEndHint(row, "box"), "--memory-max") {
		t.Errorf("the row says the cap stopped the agent: %+v, hint %q", row, cl.SessionEndHint(row, "box"))
	}
	_ = h.g.Kill()
	if m := lastRecord(t, h); m.Status.OOM || m.Life.End == nil || m.Life.End.OOMAgent || !m.Life.End.OOM {
		t.Errorf("the last record says the cap stopped the agent: %+v %+v", m.Status, m.Life.End)
	}
}
