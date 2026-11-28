package supervisor_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supervisor"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestMain(m *testing.M) { hosttest.Main(m) }

// fakeSandbox connects the supervisor directly to an in-process test guest
// over a Unix socket pair, so tests can inject forged claims or hostile frames.
type fakeSandbox struct {
	guestConn net.Conn
	hostConn  net.Conn
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	exit      supervisor.Exit
}

func newFakeSandbox(t *testing.T) *fakeSandbox {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	toConn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return &fakeSandbox{
		hostConn:  toConn(fds[0]),
		guestConn: toConn(fds[1]),
		done:      make(chan struct{}),
	}
}

func (f *fakeSandbox) Start(context.Context) (net.Conn, error) { return f.hostConn, nil }
func (f *fakeSandbox) Stop(context.Context, time.Duration) error {
	return f.Kill()
}
func (f *fakeSandbox) Kill() error {
	return f.killWith(supervisor.Exit{})
}
func (f *fakeSandbox) killWith(ex supervisor.Exit) error {
	f.once.Do(func() {
		f.mu.Lock()
		f.exit = ex
		f.mu.Unlock()
		_ = f.guestConn.Close()
		_ = f.hostConn.Close()
		close(f.done)
	})
	return nil
}
func (f *fakeSandbox) Wait() supervisor.Exit {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exit
}
func (f *fakeSandbox) Destroy() error { return nil }

func seedWalledManifest(t *testing.T, id, agentKind string) {
	t.Helper()
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema: 1, ID: id, Name: "test-" + id, CreatedAt: now, HostVersion: version.Version,
		Agent:  host.AgentInfo{Kind: agentKind, Argv: []string{"/bin/sh"}},
		Cwd:    t.TempDir(),
		Env:    []string{"USER=agent"},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall:   &host.WallRecord{Level: "none", Driver: "plain", Version: version.Version, At: now},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
}

func startFakeGuestLink(t *testing.T, sb *fakeSandbox) (*link.Session, *proto.Writer) {
	t.Helper()
	sess := link.New(sb.guestConn, link.Guest, link.Options{})
	t.Cleanup(func() { _ = sess.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctl, err := sess.Accept(ctx)
	if err != nil {
		t.Fatalf("accept ctl: %v", err)
	}
	r := bufio.NewReader(ctl)
	w := proto.NewWriter(ctl)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TRequest {
		t.Fatalf("read start request: %v (%v)", f, err)
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil || req.Op != "start" {
		t.Fatalf("expected start op, got %+v (%v)", req, err)
	}
	res, _ := json.Marshal(guest.StartResp{HolderPID: 100, AgentPID: 101, Cols: 80, Rows: 24})
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res}); err != nil {
		t.Fatal(err)
	}
	// Keep reading future ctl requests (such as stop) in the background.
	go func() {
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			if f.Type == proto.TRequest {
				var req proto.Request
				if json.Unmarshal(f.Payload, &req) == nil && req.Op == "stop" {
					_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
					return
				}
			}
		}
	}()
	return sess, w
}

func runSupervisorBG(t *testing.T, id string, sb supervisor.Sandbox, opt supervisor.Options) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- supervisor.RunWith(id, sb, opt)
		close(done)
	}()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, 2*time.Second)
		_ = sb.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("supervisor %s did not exit in cleanup", id)
		}
		// Remove the session dir so hosttest.cleanup does not try to reap
		// os.Getpid() (the in-process supervisor's PID).
		_ = os.RemoveAll(paths.SessionDir(id))
	})
	return done
}

func dialSupervisor(t *testing.T, id string) (net.Conn, *bufio.Reader, *proto.Writer) {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(context.Background(), "unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	w := proto.NewWriter(conn)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "bridge"}); err != nil {
		t.Fatal(err)
	}
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		t.Fatalf("hello reply = %v, %v", f, err)
	}
	return conn, r, w
}

func TestForgedStatusClaimShowsAsClaimed(t *testing.T) {
	hosttest.New(t)
	id := host.NewID()
	seedWalledManifest(t, id, "claude")
	sb := newFakeSandbox(t)
	_ = runSupervisorBG(t, id, sb, supervisor.Options{TickInterval: 50 * time.Millisecond})

	_, ctlW := startFakeGuestLink(t, sb)
	hosttest.WaitFor(t, 5*time.Second, "supervisor ready", func() bool {
		_, err := host.HolderRequest(id, "info", nil, time.Second)
		return err == nil
	})

	// 1. Send a forged status with control & bidi characters.
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{
		Type:    "status",
		Session: id,
		State:   host.StateNeedsInput,
		Source:  "hook",
		Reason:  "Allow\x1b[31m\u202e evil",
	})
	hosttest.WaitFor(t, 5*time.Second, "forged status reflected as claimed", func() bool {
		list, err := host.List()
		return err == nil && len(list) == 1 && list[0].State == host.StateNeedsInput && list[0].Claimed
	})
	list, err := host.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if !list[0].Claimed {
		t.Fatalf("expected Claimed=true on guest status, got %+v", list[0])
	}
	if strings.ContainsAny(list[0].Reason, "\x1b\u202e") {
		t.Fatalf("unclean reason kept: %q", list[0].Reason)
	}

	// 2. Forged status claiming "exited", foreign session ID, and forged agent_id are all dropped.
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: id, State: host.StateExited, Source: "hook"})
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "status", Session: "other222", State: host.StateIdle, Source: "hook"})
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "agent_id", Session: id, AgentID: "--dangerously-skip-permissions"})
	// Follow with a valid resize so we know the previous frames were processed.
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "resize", Session: id, Cols: 111, Rows: 33})
	hosttest.WaitFor(t, 5*time.Second, "resize processed", func() bool {
		raw, err := host.HolderRequest(id, "info", nil, time.Second)
		if err != nil {
			return false
		}
		var m host.Manifest
		return json.Unmarshal(raw, &m) == nil && m.Term.Cols == 111
	})

	raw, err := host.HolderRequest(id, "info", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var m host.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Status.State != host.StateNeedsInput || !m.Status.Claimed {
		t.Fatalf("forged events altered status: %+v", m.Status)
	}
	if m.Agent.AgentSessionID != "" {
		t.Fatalf("forged agent_id was kept: %q", m.Agent.AgentSessionID)
	}
}

func TestDisallowedFrameClosesConnection(t *testing.T) {
	hosttest.New(t)
	id := host.NewID()
	seedWalledManifest(t, id, "custom")
	sb := newFakeSandbox(t)
	_ = runSupervisorBG(t, id, sb, supervisor.Options{TickInterval: 50 * time.Millisecond})
	sess, _ := startFakeGuestLink(t, sb)

	hosttest.WaitFor(t, 5*time.Second, "supervisor socket", func() bool {
		_, err := host.HolderRequest(id, "info", nil, time.Second)
		return err == nil
	})

	// Part A: disallowed frame from client closes connection immediately.
	conn, r, w := dialSupervisor(t, id)
	if err := w.Write(proto.Type(0xfe), []byte("bad")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := proto.Read(r); err == nil {
		t.Fatal("supervisor did not close connection on disallowed client frame")
	}
	_ = conn.Close()

	// Part B: hook op on session socket is refused with forbidden.
	_, err := host.HolderRequest(id, "hook", map[string]string{"event": "stop"}, 2*time.Second)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "forbidden" {
		t.Fatalf("hook on session socket = %v, want forbidden", err)
	}

	// Part C: hostile guest sends disallowed frame type during attach -> supervisor closes connection without forwarding it.
	go func() {
		st, err := sess.Accept(context.Background())
		if err != nil {
			return
		}
		defer st.Close()
		gr := bufio.NewReader(st)
		gw := proto.NewWriter(st)
		// Holder hello handshake
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "holder"})
		// Read TAttach
		_, _ = proto.Read(gr)
		_ = gw.WriteJSON(proto.TAttached, proto.Attached{Session: id, Mode: "full", From: 0, Live: 0})
		// Inject disallowed frame type (TInput is never valid guest->bridge!)
		_ = gw.Write(proto.TInput, []byte("forged-input"))
	}()

	conn2, r2, w2 := dialSupervisor(t, id)
	defer conn2.Close()
	if err := w2.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24, LastOffset: -1}); err != nil {
		t.Fatal(err)
	}
	_ = conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := proto.Read(r2)
	if err != nil || f.Type != proto.TAttached {
		t.Fatalf("expected TAttached, got %v, %v", f, err)
	}
	if f2, err := proto.Read(r2); err == nil {
		t.Fatalf("supervisor forwarded disallowed guest frame 0x%02x instead of closing connection", byte(f2.Type))
	}

	audit, err := os.ReadFile(paths.AuditLog(id))
	if err != nil || !bytes.Contains(audit, []byte(`"frame_refused"`)) {
		t.Fatalf("audit log lacks frame_refused (%v):\n%s", err, audit)
	}
}

func TestLinkSilentRaisesAlert(t *testing.T) {
	h := hosttest.New(t)
	id := host.NewID()
	seedWalledManifest(t, id, "custom")

	eventsFile := filepath.Join(h.Home, "alerts.out")
	if _, err := host.AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{
		Events:       []string{alerts.LinkSilent},
		Delay:        "0s",
		Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + eventsFile + "; echo >> " + eventsFile}},
	}}); err != nil {
		t.Fatal(err)
	}

	sb := newFakeSandbox(t)
	_ = runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout: 120 * time.Millisecond,
		TickInterval:     40 * time.Millisecond,
	})
	// Start guest without sending heartbeats.
	startFakeGuestLink(t, sb)

	hosttest.WaitFor(t, 5*time.Second, "link_silent alert delivered", func() bool {
		b, _ := os.ReadFile(eventsFile)
		return strings.Contains(string(b), `"link_silent"`)
	})
	audit, err := os.ReadFile(paths.AuditLog(id))
	if err != nil || !bytes.Contains(audit, []byte(`"link_silent"`)) {
		t.Fatalf("audit log lacks link_silent (%v):\n%s", err, audit)
	}
}

func TestForgedGuestEndedStaysClaimedAndDoesNotBlindMonitoring(t *testing.T) {
	h := hosttest.New(t)
	id := host.NewID()
	seedWalledManifest(t, id, "custom")

	eventsFile := filepath.Join(h.Home, "alerts.out")
	if _, err := host.AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{
		Events:       []string{alerts.LinkSilent, alerts.Failed},
		Delay:        "0s",
		Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + eventsFile + "; echo >> " + eventsFile}},
	}}); err != nil {
		t.Fatal(err)
	}

	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		HeartbeatTimeout: 150 * time.Millisecond,
		TickInterval:     40 * time.Millisecond,
	})
	_, ctlW := startFakeGuestLink(t, sb)
	hosttest.WaitFor(t, 5*time.Second, "supervisor ready", func() bool {
		_, err := host.HolderRequest(id, "info", nil, time.Second)
		return err == nil
	})

	// Hostile guest sends a forged clean "ended" event while keeping the sandbox alive.
	_ = ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "ended", Session: id, ExitCode: 0})
	hosttest.WaitFor(t, 5*time.Second, "guest ended claim recorded", func() bool {
		list, err := host.List()
		return err == nil && len(list) == 1 && list[0].State == host.StateExited && list[0].Claimed
	})

	// Monitoring must stay active: link_silent must still fire when heartbeats stop.
	hosttest.WaitFor(t, 5*time.Second, "link_silent after forged ended", func() bool {
		b, _ := os.ReadFile(eventsFile)
		return strings.Contains(string(b), `"link_silent"`)
	})

	// When the sandbox later crashes, the trusted supervisor records the real
	// sandbox exit (Claimed: false, Source: "sandbox") rather than keeping the forged claim.
	_ = sb.killWith(supervisor.Exit{Code: 137, Signal: syscall.SIGKILL.String()})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit after sandbox crash")
	}
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status.Claimed || m.Status.Source != "sandbox" || m.Status.State != host.StateErrored || m.Status.ExitCode == nil || *m.Status.ExitCode != 137 {
		t.Fatalf("final status after sandbox crash = %+v, want trusted sandbox errored(137)", m.Status)
	}
}

func TestGuestRepliesCleanedAndStalledUploadTimesOut(t *testing.T) {
	hosttest.New(t)
	id := host.NewID()
	seedWalledManifest(t, id, "custom")
	sb := newFakeSandbox(t)
	_ = runSupervisorBG(t, id, sb, supervisor.Options{
		TickInterval:  50 * time.Millisecond,
		UploadTimeout: 200 * time.Millisecond,
	})
	sess, _ := startFakeGuestLink(t, sb)
	hosttest.WaitFor(t, 5*time.Second, "supervisor ready", func() bool {
		_, err := host.HolderRequest(id, "info", nil, time.Second)
		return err == nil
	})

	// Serve hostile guest streams:
	// 1. Holder stream for "ports": malformed non-array Result (must fail closed).
	// 2. Holder stream for "send": error containing ANSI/bidi escapes and bogus Code/Data (must be cleaned).
	// 3. KindPut stream: error containing ANSI/bidi escapes (must be cleaned).
	// 4. KindPut stream: stalls without reading or replying (must time out cleanly).
	go func() {
		for i := range 4 {
			st, err := sess.Accept(context.Background())
			if err != nil {
				return
			}
			switch st.Kind() {
			case link.KindHolder:
				gr := bufio.NewReader(st)
				gw := proto.NewWriter(st)
				_, _ = proto.Read(gr)
				_ = gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "holder"})
				f, err := proto.Read(gr)
				if err != nil {
					_ = st.Close()
					continue
				}
				var req proto.Request
				_ = json.Unmarshal(f.Payload, &req)
				switch req.Op {
				case "ports":
					_ = gw.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{"evil":"\u001b[31m"}`)})
				case "send":
					_ = gw.WriteJSON(proto.TResponse, proto.Response{
						ID: req.ID, OK: false,
						Error: &proto.Error{
							Code:    "BAD\x1b[31mCODE",
							Message: "boom\x1b[31m\u202e",
							Hint:    "try\x1b[31mthis",
							Data:    json.RawMessage(`{"leaked":true}`),
						},
					})
				}
				_ = st.Close()
			case link.KindPut:
				if i == 2 {
					_, _ = io.Copy(io.Discard, st)
					b, _ := json.Marshal(guest.PutResp{
						Error: &proto.Error{
							Code:    "BAD CODE",
							Message: "put\x1b[31m\u202eerr",
							Hint:    "hint\x1b[31m",
							Data:    json.RawMessage(`{"x":1}`),
						},
					})
					_, _ = st.Write(b)
					_ = st.Close()
				} else {
					// Stall without ever replying; cleaned up when session closes.
					go func(s *link.Stream) {
						<-sess.Done()
						_ = s.Close()
					}(st)
				}
			default:
				_ = st.Close()
			}
		}
	}()

	// 1. Malformed ports fails closed with protocol error.
	_, err := host.HolderRequest(id, "ports", nil, 2*time.Second)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "protocol" {
		t.Fatalf("malformed ports = %v, want protocol error", err)
	}

	// 2. Hostile error on send is cleaned and Data is dropped.
	_, err = host.HolderRequest(id, "send", map[string]any{"text": "hi"}, 2*time.Second)
	if !errors.As(err, &pe) || pe.Code != "holder" || strings.ContainsAny(pe.Message+pe.Hint, "\x1b\u202e") || len(pe.Data) != 0 {
		t.Fatalf("uncleaned send error = %+v", pe)
	}

	// 3. Hostile error on KindPut is cleaned and Data is dropped.
	upload := func(reqID uint64, body []byte) proto.Response {
		t.Helper()
		conn, r, w := dialSupervisor(t, id)
		defer conn.Close()
		sum := sha256.Sum256(body)
		_ = w.WriteJSON(proto.TUploadBegin, proto.UploadBegin{ID: reqID, Session: id, Name: "a.png", Size: int64(len(body))})
		_ = w.Write(proto.TUploadChunk, proto.EncodeChunk(reqID, body))
		_ = w.WriteJSON(proto.TUploadEnd, proto.UploadEnd{ID: reqID, SHA256: hex.EncodeToString(sum[:])})
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		f, err := proto.Read(r)
		if err != nil || f.Type != proto.TResponse {
			t.Fatalf("upload %d reply = %v, %v", reqID, f, err)
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := upload(1, []byte("abc"))
	if resp.OK || resp.Error == nil || resp.Error.Code != "upload" ||
		strings.ContainsAny(resp.Error.Message+resp.Error.Hint, "\x1b\u202e") || len(resp.Error.Data) != 0 {
		t.Fatalf("uncleaned upload error = %+v", resp)
	}

	// 4. Stalled KindPut stream times out cleanly with an upload error.
	resp2 := upload(2, []byte("xyz"))
	if resp2.OK || resp2.Error == nil || resp2.Error.Code != "upload" {
		t.Fatalf("stalled upload reply = %+v, want upload timeout error", resp2)
	}
}
