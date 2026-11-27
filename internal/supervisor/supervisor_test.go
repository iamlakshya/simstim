package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A forged status claim shows as claimed: in the trusted manifest and in
// the session's row. Its strings are cleaned, and a claim about another
// session changes nothing.
func TestAForgedStatusShowsAsClaimed(t *testing.T) {
	id, g, _ := supervised(t)
	g.send(proto.CtlEvent{Event: proto.EventStatus, Session: "zzzz2222", State: host.StateIdle, Source: "quiet"})
	g.send(proto.CtlEvent{Event: proto.EventStatus, Session: id, State: host.StateNeedsInput, Source: "notify",
		Reason: "\x1b]0;evil\x07Proceed?\u202e"})
	var m *host.Manifest
	waitFor(t, "the claim", func() bool {
		var err error
		m, err = host.LoadManifest(id)
		return err == nil && m.Status.State == host.StateNeedsInput
	})
	if !m.Status.Claimed || m.Status.Source != "notify" || m.Guest == nil || m.Guest.State != host.StateNeedsInput {
		t.Errorf("status %+v, claims %+v: want a claimed needs_input", m.Status, m.Guest)
	}
	if strings.ContainsAny(m.Status.Reason, "\x1b\x07\u202e") || !strings.Contains(m.Status.Reason, "Proceed?") {
		t.Errorf("the reason is not cleaned: %q", m.Status.Reason)
	}
	list, err := host.List()
	if err != nil || len(list) != 1 || !list[0].Claimed || list[0].State != host.StateNeedsInput || list[0].Wall == nil {
		t.Errorf("the row = %+v, %v: want a claimed needs_input, with its wall", list, err)
	}
	// The end is a claim too, and a later status changes nothing.
	code := 3
	g.send(proto.CtlEvent{Event: proto.EventEnded, Session: "zzzz2222", ExitCode: &code})
	g.send(proto.CtlEvent{Event: proto.EventEnded, Session: id, ExitCode: &code})
	g.send(proto.CtlEvent{Event: proto.EventStatus, Session: id, State: host.StateWorking, Source: "output"})
	waitFor(t, "the end", func() bool {
		m, err = host.LoadManifest(id)
		return err == nil && m.Status.State == host.StateErrored
	})
	time.Sleep(100 * time.Millisecond)
	if m, _ = host.LoadManifest(id); !m.Status.Claimed || m.Status.ExitCode == nil || *m.Status.ExitCode != 3 || m.Status.State != host.StateErrored {
		t.Errorf("after the end: %+v", m.Status)
	}
}

// The agent's own session ID is kept only in the agent's form.
func TestAnAgentIDMustHaveTheAgentsForm(t *testing.T) {
	id, g, _ := supervised(t)
	g.send(proto.CtlEvent{Event: proto.EventAgentID, Session: id, ID: "--dangerously-skip-permissions"})
	g.send(proto.CtlEvent{Event: proto.EventAgentID, Session: id, ID: "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"})
	waitFor(t, "the agent's ID", func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && m.Agent.AgentSessionID == "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	})
	if m, _ := host.LoadManifest(id); len(m.Agent.IDHistory) != 1 {
		t.Errorf("history = %+v, want the one ID of the agent's form", m.Agent.IDHistory)
	}
}

// A frame of a type outside the allowlist closes the connection, in either
// direction (spec 6.3). Frames inside it pass, byte for byte.
func TestAFrameOutsideTheAllowlistClosesTheConnection(t *testing.T) {
	id, g, _ := supervised(t)
	conn, r, w := dial(t, id)
	if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24, LastOffset: -1}); err != nil {
		t.Fatal(err)
	}
	st := <-g.holders
	gr, gw := bufio.NewReader(st), proto.NewWriter(st)
	if f, err := proto.Read(gr); err != nil || f.Type != proto.THello {
		t.Fatalf("the holder got %v, %v, want the client's hello", f.Type, err)
	}
	_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
	if f, err := proto.Read(gr); err != nil || f.Type != proto.TAttach {
		t.Fatalf("the holder got %v, %v, want the attach", f.Type, err)
	}
	out := proto.EncodeOutput(0, []byte("\x1b[?1049h\x00\xff\x1bc"))
	_ = gw.Write(proto.TOutput, out)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TOutput || string(f.Payload) != string(out) {
		t.Fatalf("the client got %v %q, %v, want the output frame as it was", f.Type, f.Payload, err)
	}
	// An upload frame never comes from a holder.
	_ = gw.Write(proto.TUploadBegin, []byte(`{}`))
	refused(t, r)
	conn.Close()

	// A frame from the client that may not pass to the guest closes the
	// connection too, before any holder stream opens.
	_, r2, w2 := dial(t, id)
	_ = w2.Write(proto.TUploadChunk, []byte("12345678data"))
	if f, err := proto.Read(r2); !errors.Is(err, io.EOF) {
		t.Fatalf("after a frame that may not pass, the client read %v, %v, want the end", f.Type, err)
	}
	select {
	case <-g.holders:
		t.Error("a holder stream opened for a frame that may not pass")
	case <-time.After(100 * time.Millisecond):
	}
}

// The supervisor answers some requests itself, from the trusted manifest,
// passes peek to the holder, and refuses hook and the rest.
func TestTheSupervisorAnswersFromTheTrustedManifest(t *testing.T) {
	id, g, _ := supervised(t)
	raw, err := host.HolderRequest(id, "info", nil, 3*time.Second)
	var m host.Manifest
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Wall == nil || m.Run.HolderPID != os.Getpid() || m.Guest.AgentPID != 7 || m.Run.AgentPID != 0 {
		t.Fatalf("info = %s, %v", raw, err)
	}
	if _, err := host.HolderRequest(id, "rename", map[string]string{"name": "renamed"}, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	for op, code := range map[string]string{"hook": "forbidden", "secrets": "started", "debug.start": "bad_request", "debug.profile": "unknown_op", "no-such-op": "unknown_op"} {
		_, err := host.HolderRequest(id, op, nil, 3*time.Second)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != code || pe.Hint == "" {
			t.Errorf("%s = %v, want %s with a hint", op, err, code)
		}
	}
	// peek is the holder's: the guest answers it.
	go func() {
		st := <-g.holders
		gr, gw := bufio.NewReader(st), proto.NewWriter(st)
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"})
		f, _ := proto.Read(gr)
		var req proto.Request
		_ = json.Unmarshal(f.Payload, &req)
		_ = gw.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{"data":"aGk="}`)})
	}()
	if raw, err := host.HolderRequest(id, "peek", nil, 3*time.Second); err != nil || string(raw) != `{"data":"aGk="}` {
		t.Errorf("peek = %s, %v", raw, err)
	}
	waitFor(t, "the new name", func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && m.Name == "renamed"
	})
	b, _ := os.ReadFile(paths.AuditLog(id))
	for _, ev := range []string{`"event":"start"`, `"event":"rename"`} {
		if !strings.Contains(string(b), ev) {
			t.Errorf("the audit log lacks %s:\n%s", ev, b)
		}
	}
}

// A kill stops the sandbox through the ctl stream, and the supervisor
// exits with the session's last save.
func TestAKillStopsTheSandbox(t *testing.T) {
	id, g, ended := supervised(t)
	if _, err := host.HolderRequest(id, "kill", nil, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not exit")
	}
	g.mu.Lock()
	ops := strings.Join(g.ops, ",")
	g.mu.Unlock()
	if ops != "start,stop" {
		t.Errorf("the guest got %s, want start,stop", ops)
	}
	m, err := host.LoadManifest(id)
	if err != nil || m.Status.State != host.StateExited || m.Status.Reason != "killed by request" {
		t.Errorf("after the kill: %+v, %v", m.Status, err)
	}
	if _, err := os.Stat(paths.Socket(id)); !os.IsNotExist(err) {
		t.Errorf("the socket is still there: %v", err)
	}
	if b, _ := os.ReadFile(paths.AuditLog(id)); !strings.Contains(string(b), `"event":"stop"`) {
		t.Errorf("the audit log has no stop:\n%s", b)
	}
}

// An event over 64 KiB is a breach: the supervisor closes the link, and
// the session ends as an error, with sandbox_error and, since the guest
// claimed no end, failed.
func TestAnEventOverTheCapClosesTheLink(t *testing.T) {
	id, g, ended := supervised(t)
	got := alertsTo(t)
	g.mu.Lock()
	st := g.ctlSt
	g.mu.Unlock()
	// Rejection must follow the oversized header, before any payload is
	// allocated or sent. Requiring a whole rejected payload to write
	// successfully races the correct early close of the link.
	writeHeaderOnly(t, st, proto.TEvent, maxEvent)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not close the link")
	}
	m, err := host.LoadManifest(id)
	if err != nil || m.Status.State != host.StateErrored || m.Status.Reason != "the sandbox broke the link's rules" || m.Status.Claimed {
		t.Errorf("after the breach: %+v, %v", m.Status, err)
	}
	if b, _ := os.ReadFile(paths.AuditLog(id)); !strings.Contains(string(b), `"event":"link_breach"`) {
		t.Errorf("the audit log has no breach:\n%s", b)
	}
	waitAlerts(t, got, alerts.SandboxError, alerts.Failed)
	list, _ := host.List()
	if len(list) != 1 || list[0].State != host.StateErrored || list[0].Claimed {
		t.Errorf("the row after a breach = %+v", list)
	}
}
