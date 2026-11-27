package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/brand"
	cl "github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// realEndedGate stops the supervisor's transport reads after startup. The
// guest's actual link writer, event reader and holder then meet backpressure.
type realEndedGate struct {
	net.Conn
	paused      atomic.Bool
	blocked     chan struct{}
	released    chan struct{}
	closed      chan struct{}
	blockOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
}

func (g *realEndedGate) Read(p []byte) (int, error) {
	if g.paused.Load() {
		g.blockOnce.Do(func() { close(g.blocked) })
		select {
		case <-g.released:
		case <-g.closed:
			return 0, net.ErrClosed
		}
	}
	return g.Conn.Read(p)
}

func (g *realEndedGate) release() {
	g.releaseOnce.Do(func() { close(g.released) })
}

func (g *realEndedGate) Close() error {
	g.closeOnce.Do(func() { close(g.closed); g.release() })
	return g.Conn.Close()
}

// realEndedSandbox runs the real guest init in this test process. The init
// starts the real holder subprocess through this package's TestMain.
type realEndedSandbox struct {
	root     string
	mu       sync.Mutex
	gate     *realEndedGate
	guest    net.Conn
	stopped  bool
	done     chan struct{}
	doneOnce sync.Once
}

func (s *realEndedSandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.gate != nil {
		return nil, net.ErrClosed
	}
	a, b := net.Pipe()
	s.guest = b
	s.gate = &realEndedGate{Conn: a, blocked: make(chan struct{}), released: make(chan struct{}), closed: make(chan struct{})}
	init := guest.New(b, s.root, slog.New(slog.DiscardHandler))
	go func() { //nolint:contextcheck // Start bounds boot only. The real init serves until its link closes.
		init.Serve()
		s.doneOnce.Do(func() { close(s.done) })
	}()
	return s.gate, nil
}

func (s *realEndedSandbox) Kill() error {
	s.mu.Lock()
	s.stopped = true
	gate, peer := s.gate, s.guest
	s.mu.Unlock()
	if gate == nil {
		s.doneOnce.Do(func() { close(s.done) })
		return nil
	}
	gate.release()
	_ = gate.Close()
	_ = peer.Close()
	return nil
}

func (s *realEndedSandbox) Wait() error { <-s.done; return nil }

func (s *realEndedSandbox) transport() *realEndedGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gate
}

func realEndedHook(t *testing.T, sock string) (net.Conn, *bufio.Reader, *proto.Writer) {
	t.Helper()
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	r, w := bufio.NewReader(c), proto.NewWriter(c)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "hook"}); err != nil {
		t.Fatal(err)
	}
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		t.Fatalf("hook hello: type=%v err=%v", f.Type, err)
	}
	return c, r, w
}

func realEndedSendHook(t *testing.T, r *bufio.Reader, w *proto.Writer, seq uint64, event host.HookEvent) {
	t.Helper()
	args, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: seq, Op: "hook", Args: args}); err != nil {
		t.Fatalf("hook %d write: %v", seq, err)
	}
	f, err := proto.Read(r)
	var response proto.Response
	if err != nil || f.Type != proto.TResponse || json.Unmarshal(f.Payload, &response) != nil || !response.OK {
		t.Fatalf("hook %d answer: type=%v response=%+v err=%v", seq, f.Type, response, err)
	}
}

// A temporary transport stall backpressures the real init's fd 3 reader and
// the real holder's event queue. Its end still crosses both components and
// reaches the trusted list, client list and end alert as a guest claim.
func TestRealGuestInitPreservesEndedAfterEventBackpressure(t *testing.T) {
	dir := testFolders(t)
	gotAlerts := alertEventsTo(t)
	const id = "real2345"
	exitFlag := filepath.Join(dir, "end-agent")
	sb := &realEndedSandbox{root: filepath.Join(dir, "guest"), done: make(chan struct{})}
	old := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) { return sb, nil }
	t.Cleanup(func() { sandboxFor = old })
	t.Cleanup(func() { _ = sb.Kill() })
	started := make(chan *Supervisor, 1)
	oldStarted := testStarted
	testStarted = func(s *Supervisor) { started <- s }
	t.Cleanup(func() { testStarted = oldStarted })
	now := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: now, Cwd: dir,
		Agent: host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh", "-c",
			`while [ ! -f "$1" ]; do sleep 0.01; done; exit 7`, "agent-exit", exitFlag}},
		Env: []string{"PATH=/usr/bin:/bin", "HOME=" + host.GuestHome,
			brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(), "TERM=xterm-256color"},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall:   &host.WallRecord{Level: "none", Driver: driverPlain, Version: version.Version, At: now}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { _ = Run(id); close(ended) }()
	t.Cleanup(func() {
		if gate := sb.transport(); gate != nil {
			gate.release()
		}
		_, _ = host.HolderRequest(id, "kill", nil, time.Second)
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			_ = sb.Kill()
			select {
			case <-ended:
			case <-time.After(10 * time.Second):
				t.Error("the real guest init's supervisor did not stop")
			}
		}
	})
	waitFor(t, "the real guest init starting its holder and agent", func() bool {
		cur, err := host.LoadManifest(id)
		return err == nil && cur.Status.State == host.StateWorking
	})
	var running *Supervisor
	select {
	case running = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the real supervisor did not finish startup")
	}
	files := guest.Files(sb.root)
	c, r, w := realEndedHook(t, files.Hook)
	gate := sb.transport()
	gate.paused.Store(true)
	makeID := func(n int) string { return fmt.Sprintf("3f2b8c1e-9d4a-4e6b-8f7c-%012x", n) }
	sessionHook := func(seq uint64, n int) {
		payload, _ := json.Marshal(map[string]string{"session_id": makeID(n), "source": "startup"})
		realEndedSendHook(t, r, w, seq, host.HookEvent{Event: "session-start", Payload: payload})
	}
	sessionHook(1, 1)
	select {
	case <-gate.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the real guest link did not reach the paused transport")
	}
	realEndedSendHook(t, r, w, 2, host.HookEvent{Event: "notification", Payload: json.RawMessage(`{"message":"waiting"}`)})
	waitFor(t, "the real holder's notification status while transport is paused", func() bool {
		cur, err := host.LoadManifestFile(files.Manifest)
		return err == nil && cur.Status.State == host.StateNeedsInput && cur.Status.Source == "notify"
	})
	// These events exceed both the link's fixed window and the holder's
	// 256-slot queue while the supervisor transport cannot take more bytes.
	const hookCount = 3200
	for n := 2; n <= hookCount; n++ {
		sessionHook(uint64(n+1), n)
	}
	_ = c.Close()
	if err := os.WriteFile(exitFlag, []byte("exit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the real holder recording exit behind transport backpressure", func() bool {
		cur, err := host.LoadManifestFile(files.Manifest)
		return err == nil && cur.Status.ExitCode != nil && *cur.Status.ExitCode == 7
	})
	gate.release()
	waitFor(t, "the real guest's claimed end in the trusted host list", func() bool {
		rows, err := host.List()
		return err == nil && len(rows) == 1 && rows[0].State == host.StateErrored && rows[0].Claimed &&
			rows[0].ExitCode != nil && *rows[0].ExitCode == 7
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := cl.Dial(ctx, bridgeHost(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.Sessions(ctx)
	if err != nil || len(rows) != 1 || !cl.Ended(rows[0]) || !rows[0].Claimed || rows[0].ExitCode == nil || *rows[0].ExitCode != 7 {
		t.Fatalf("real guest client sessions: %+v, %v", rows, err)
	}
	select {
	case ev := <-gotAlerts:
		if ev.Event != alerts.Failed || !ev.Claimed || ev.ExitCode == nil || *ev.ExitCode != 7 || !strings.HasSuffix(ev.Summary, "(claimed)") {
			t.Fatalf("real guest end alert: %+v", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the real guest's claimed end alert was not recorded")
	}
	// Stop through the supervisor's existing signal channel. Its real init
	// must stop the holder, drain its events and close the link before the
	// supervisor saves the claimed end for restore.
	running.sig <- syscall.SIGTERM
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the real init did not finish the supervisor's system stop")
	}
	listed, err := host.List()
	if err != nil || len(listed) != 1 || listed[0].State != host.StateErrored || !listed[0].Claimed ||
		listed[0].ExitCode == nil || *listed[0].ExitCode != 7 {
		t.Fatalf("saved real guest host list: %+v, %v", listed, err)
	}
	saved, err := host.LoadManifest(id)
	if err != nil || !saved.Ended() {
		t.Fatalf("the stopped real guest remains eligible for restore: %+v, %v", saved, err)
	}
	rows, err = conn.Sessions(ctx)
	if err != nil || len(rows) != 1 || !cl.Ended(rows[0]) || !rows[0].Claimed || rows[0].ExitCode == nil || *rows[0].ExitCode != 7 {
		t.Fatalf("saved real guest client sessions: %+v, %v", rows, err)
	}
	delivery := &alerts.DeliveryLog{Path: paths.AlertsLog()}
	entries, err := delivery.Tail(16)
	if err != nil {
		t.Fatal(err)
	}
	ends := 0
	for _, entry := range entries {
		if entry.ID == id && entry.Event == alerts.Failed && entry.OK {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("recorded real guest end deliveries=%d, entries=%+v", ends, entries)
	}
}
