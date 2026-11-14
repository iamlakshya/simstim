package supervisor

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A forged end of the agent stays a claim: the trusted memory watch goes
// on, the warning, the kill at the cap and its audit line still come, and
// the last record is the trusted end, a kill by request.
func TestAForgedEndLeavesTheTrustedFactsOn(t *testing.T) {
	set := fakeCgroup(t)
	h := hostileStart(t)
	h.answerStart(t)
	zero := 0
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &zero})
	status := func(what string, want func(host.Status) bool) {
		t.Helper()
		waitFor(t, what, func() bool {
			m, err := host.LoadManifest(h.id)
			return err == nil && want(m.Status)
		})
	}
	status("the claimed end", func(s host.Status) bool { return s.State == host.StateExited && s.Claimed })
	set("memory.current", strconv.Itoa((1<<30)/100*96))
	status("the memory warning", func(s host.Status) bool { return s.Source == host.SrcMemory && !s.Claimed })
	set("memory.events", "max 3\noom 1\noom_kill 1")
	// After the claimed end, a kill at the cap shows by its reason: the mark
	// on an ended agent means its own kill.
	status("the kill at the cap", func(s host.Status) bool { return s.Reason == host.OOMChildReason && !s.OOM })
	waitFor(t, "the audit line", func() bool {
		b, _ := os.ReadFile(paths.AuditLog(h.id))
		return strings.Contains(string(b), `"event":"oom_kill"`)
	})
	if _, err := host.HolderRequest(h.id, "kill", nil, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	_ = h.waitEnd(t, 15*time.Second)
	m, _ := host.LoadManifest(h.id)
	if m.Status.Reason != "killed by request" || m.Status.Claimed || m.Status.OOM || m.Life.End == nil || !m.Life.End.OOM {
		t.Errorf("the last record keeps the claim, not the trusted end: %+v %+v", m.Status, m.Life.End)
	}
	if m.Guest.ExitCode == nil || *m.Guest.ExitCode != 0 {
		t.Errorf("the claimed end is gone from the claims: %+v", m.Guest)
	}
}

// A guest cannot forge an end out of memory: without a kill in the
// unit's count, neither the end frame that a client gets nor the session's
// row says out of memory, and the end frame says that it is claimed.
func TestAGuestCannotForgeAnOOMEnd(t *testing.T) {
	fakeCgroup(t)
	h := hostileStart(t)
	h.answerStart(t)
	conn, r, w := dial(t, h.id)
	_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
	st := <-h.g.holders
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.TEnded, proto.Ended{ExitCode: 137, Signal: "killed", OOM: true})
	f, err := proto.Read(r)
	var e proto.Ended
	if err != nil || f.Type != proto.TEnded || json.Unmarshal(f.Payload, &e) != nil {
		t.Fatalf("the client got %v %s, %v", f.Type, f.Payload, err)
	}
	if e.OOM || !e.Claimed || e.ExitCode != 137 || e.Signal != "killed" {
		t.Errorf("the end the client got = %+v, want a claimed SIGKILL and no OOM", e)
	}
	conn.Close()
	code := 137
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
	waitFor(t, "the claimed end", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.ExitCode != nil
	})
	if m, _ := host.LoadManifest(h.id); m.Status.OOM || m.Status.Reason == "oom" || !m.Status.Claimed {
		t.Errorf("a claimed SIGKILL with no kill at the cap = %+v", m.Status)
	}
}

// The agent's own kill at the cap is out of memory when its end comes
// before a tick claims the kill. When a tick claimed it for a child first,
// the row says so by its reason, and its out-of-memory mark, which on an
// ended agent means the agent's own, stays off, as a holder's does.
func TestTheOrderOfATickAndTheEndKeepsTheFacts(t *testing.T) {
	t.Run("end first", func(t *testing.T) {
		set := fakeCgroup(t)
		h := hostileStart(t)
		h.answerStart(t)
		set("memory.events", "max 3\noom 1\noom_kill 1")
		code := 137
		h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
		waitFor(t, "the agent's end at the cap", func() bool {
			m, err := host.LoadManifest(h.id)
			return err == nil && m.Status.OOM && m.Status.Reason == "oom" && m.Status.State == host.StateErrored
		})
	})
	t.Run("tick first", func(t *testing.T) {
		set := fakeCgroup(t)
		h := hostileStart(t)
		h.answerStart(t)
		set("memory.events", "max 3\noom 1\noom_kill 1")
		waitFor(t, "a child's kill", func() bool {
			m, err := host.LoadManifest(h.id)
			return err == nil && m.Status.OOM && m.Status.Reason == host.OOMChildReason
		})
		code := 137
		h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code, Signal: "killed"})
		waitFor(t, "the end", func() bool {
			m, err := host.LoadManifest(h.id)
			return err == nil && m.Status.ExitCode != nil
		})
		if m, _ := host.LoadManifest(h.id); m.Status.OOM || m.Status.Reason != host.OOMChildReason {
			t.Errorf("the row after a child's kill and the agent's SIGKILL: %+v", m.Status)
		}
	})
}

// A guest that grants no credit cannot hold the start past its limit: the
// supervisor closes the link, and the session fails, with sandbox_error.
func TestAStalledStartLosesItsLink(t *testing.T) {
	old := bootTimeout
	bootTimeout = 2 * time.Second
	t.Cleanup(func() { bootTimeout = old })
	h := hostileStart(t, "BIG="+strings.Repeat("x", 3*link.DefaultWindow))
	got := alertsTo(t)
	// The guest never reads the ctl stream, so it gives no credit.
	start := time.Now()
	err := h.waitEnd(t, 10*time.Second)
	if err == nil || time.Since(start) > 6*time.Second {
		t.Fatalf("a stalled start = %v after %s", err, time.Since(start))
	}
	m, _ := host.LoadManifest(h.id)
	if m.Status.State != host.StateErrored {
		t.Errorf("after a stalled start: %+v", m.Status)
	}
	waitAlerts(t, got, alerts.SandboxError)
}

// A guest that half-closes the ctl stream before it answers start fails the
// start with its reason, and nothing panics.
func TestAHalfClosedCtlStreamFailsTheStart(t *testing.T) {
	h := hostileStart(t)
	_ = h.readReq(t)
	if err := h.ctl.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	err := h.waitEnd(t, 10*time.Second)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Hint == "" || !strings.Contains(pe.Message, "ctl stream") {
		t.Fatalf("a half-closed ctl stream = %v", err)
	}
	if m, _ := host.LoadManifest(h.id); m.Status.State != host.StateErrored {
		t.Errorf("after a half-closed ctl stream: %+v", m.Status)
	}
}

// The rate never drops the agent's end: after a burst of claims, the end
// still records its exit code and raises failed. When the sandbox then
// ends, the session ends as an error, which says what the guest claimed.
func TestTheAgentsEndPassesTheRate(t *testing.T) {
	h := hostileStart(t)
	got := alertsTo(t)
	h.answerStart(t)
	for i := range 25 {
		h.send(t, proto.CtlEvent{Event: proto.EventStatus, Session: h.id, State: []string{"working", "idle"}[i%2], Source: "screen"})
	}
	code := 2
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code})
	waitFor(t, "the end", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.ExitCode != nil && *m.Status.ExitCode == 2
	})
	waitAlerts(t, got, alerts.Failed)
	_ = h.g.Kill()
	_ = h.waitEnd(t, 10*time.Second)
	m, _ := host.LoadManifest(h.id)
	if m.Status.State != host.StateErrored || !strings.Contains(m.Status.Reason, "exit code 2") {
		t.Errorf("after the sandbox's end: %+v", m.Status)
	}
	waitAlerts(t, got, alerts.SandboxError)
}

// Events before the answer to start wait for it, within a small bound.
func TestEarlyEventsAreCapped(t *testing.T) {
	s := &Supervisor{m: &host.Manifest{}}
	big := make([]byte, maxEvent-1)
	for range 200 {
		s.event(big)
	}
	if s.earlyBytes > maxEarlyBytes || len(s.early)*len(big) > maxEarlyBytes || len(s.early) == 0 {
		t.Errorf("%d early events of %d bytes wait, over %d bytes", len(s.early), len(big), maxEarlyBytes)
	}
}

// A guest that sends nothing for 15 s raises link_silent, once, with its
// audit line.
func TestASilentGuestRaisesLinkSilent(t *testing.T) {
	var skew atomic.Int64
	old := now
	now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	t.Cleanup(func() { now = old })
	id, _, _ := supervised(t)
	got := alertsTo(t)
	skew.Store(int64(linkSilent + time.Second))
	waitAlerts(t, got, alerts.LinkSilent)
	if b, _ := os.ReadFile(paths.AuditLog(id)); !strings.Contains(string(b), `"event":"link_silent"`) {
		t.Errorf("the audit log has no silence:\n%s", b)
	}
}

// refused fails the test unless the client reads that the guest sent a
// frame that may not pass, then the connection's end.
func refused(t *testing.T, r *bufio.Reader) {
	t.Helper()
	f, err := proto.Read(r)
	var pe proto.Error
	if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Code != "guest_frame" || pe.Hint == "" {
		t.Fatalf("after a frame that may not pass, the client read %v %s, %v, want the refusal", f.Type, f.Payload, err)
	}
	if f, err := proto.Read(r); !errors.Is(err, io.EOF) {
		t.Fatalf("after the refusal, the client read %v, %v, want the end", f.Type, err)
	}
}

// Frames that a holder never sends a client close the connection: a
// second hello, and an event.
func TestFramesAHolderNeverSendsCloseTheConnection(t *testing.T) {
	for _, ty := range []proto.Type{proto.THello, proto.TEvent} {
		t.Run(strconv.Itoa(int(ty)), func(t *testing.T) {
			id, g, _ := supervised(t)
			_, r, w := dial(t, id)
			_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24, LastOffset: -1})
			st := <-g.holders
			gr, gw := bufio.NewReader(st), proto.NewWriter(st)
			_, _ = proto.Read(gr)
			_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
			_, _ = proto.Read(gr)
			_ = gw.Write(ty, []byte(`{}`))
			refused(t, r)
		})
	}
}

// Malformed events and answers change nothing, and the link stays. A
// request from the guest on the ctl stream is a breach.
func TestMalformedEventsKeepTheLink(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for _, p := range []string{`null`, `[]`, `"x"`, `{"event":1}`, strings.Repeat("[", 60000),
		`{"event":"status","session":"abcd2345","state":"idle","source":"q","reason":"\xff\xfe\x1b]52;c;aGk=\x07"}`,
		`{"event":"ended","session":"abcd2345","exit_code":"0"}`, `{"event":"ended","session":"abcd2345","exit_code":1e30}`,
		`{"event":"status","session":"abcd2345","state":"idle","source":"q","full_screen":"yes"}`} {
		_ = h.w.Write(proto.TEvent, []byte(p))
	}
	_ = h.w.Write(proto.TResponse, []byte(`{"id":18446744073709551615,"ok":true}`))
	_ = h.w.Write(proto.TResponse, []byte(`garbage`))
	time.Sleep(300 * time.Millisecond)
	if _, err := host.HolderRequest(h.id, "info", nil, 3*time.Second); err != nil {
		t.Fatalf("the session went after malformed events: %v", err)
	}
	if m, _ := host.LoadManifest(h.id); m.Status.ExitCode != nil || strings.ContainsAny(m.Status.Reason, "\x1b\x07") {
		t.Errorf("a malformed event changed the session: %+v", m.Status)
	}
	_ = h.w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "kill"})
	_ = h.waitEnd(t, 10*time.Second)
	if m, _ := host.LoadManifest(h.id); m.Status.Reason != "the sandbox broke the link's rules" {
		t.Errorf("after a request from the guest: %+v", m.Status)
	}
}

// A guest that dies before it answers start fails the start at once.
func TestAGuestThatDiesBeforeItsAnswer(t *testing.T) {
	h := hostileStart(t)
	_ = h.readReq(t)
	_ = h.g.Kill()
	if err := h.waitEnd(t, 10*time.Second); err == nil {
		t.Error("the start worked without an answer")
	}
	if m, _ := host.LoadManifest(h.id); m.Status.State != host.StateErrored {
		t.Errorf("after the guest died: %+v", m.Status)
	}
}

// A guest that dies in the middle of a holder frame ends the client's
// connection: the client never reads that frame whole.
func TestAGuestThatDiesMidFrame(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	_, r, w := dial(t, h.id)
	_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
	st := <-h.g.holders
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	_, _ = proto.Read(gr)
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	_, _ = proto.Read(gr)
	var hd [5]byte
	binary.LittleEndian.PutUint32(hd[:4], 1001)
	hd[4] = byte(proto.TOutput)
	_, _ = st.Write(hd[:])
	_, _ = st.Write([]byte("abc"))
	time.Sleep(200 * time.Millisecond)
	_ = h.g.Kill()
	if f, err := proto.Read(r); err == nil {
		t.Fatalf("the client read a whole frame %v %q from a guest that died in it", f.Type, f.Payload)
	}
	_ = h.waitEnd(t, 10*time.Second)
}

// Attaches that their clients close free their holder streams: the
// supervisor's 32 streams never run out. The guest sees each stream close,
// and closes its own end, as the guest init does.
func TestClosedAttachesFreeTheirStreams(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for i := range 40 {
		conn, err := net.Dial("unix", paths.Socket(h.id))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		w, r := proto.NewWriter(conn), bufio.NewReader(conn)
		_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
		_, _ = proto.Read(r)
		_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: h.id, Cols: 80, Rows: 24, LastOffset: -1})
		var st *link.Stream
		select {
		case st = <-h.g.holders:
		case <-time.After(12 * time.Second):
			t.Fatalf("attach %d: no holder stream", i+1)
		}
		gr, gw := bufio.NewReader(st), proto.NewWriter(st)
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
		_, _ = proto.Read(gr)
		conn.Close()
		// The guest sees the stream close.
		_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.Copy(io.Discard, st); errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("attach %d: the supervisor keeps the holder stream after its client left", i+1)
		}
		_ = st.Close()
	}
	if _, err := host.HolderRequest(h.id, "info", nil, 3*time.Second); err != nil {
		t.Errorf("info after the attaches: %v", err)
	}
}
