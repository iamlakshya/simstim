package supervisor

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type pausedInitSandbox struct {
	root      string
	files     host.GuestHolder
	resumeFD3 chan struct{}
	sawEnded  atomic.Bool

	mu         sync.Mutex
	linkSess   *link.Session
	holderCmd  *exec.Cmd
	holderDone chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

func (p *pausedInitSandbox) Start(startCtx context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	p.done = make(chan struct{})
	ls := link.New(b, link.Guest, link.Options{})
	p.mu.Lock()
	p.linkSess = ls
	p.mu.Unlock()
	go p.serve(context.WithoutCancel(startCtx), ls)
	return a, nil
}

func (p *pausedInitSandbox) Kill() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		cmd := p.holderCmd
		hd := p.holderDone
		ls := p.linkSess
		p.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if hd != nil {
				select {
				case <-hd:
				case <-time.After(4 * time.Second):
					_ = cmd.Process.Kill()
					<-hd
				}
			}
		}
		if ls != nil {
			_ = ls.Close()
		}
		if p.done != nil {
			close(p.done)
		}
	})
	return nil
}

func (p *pausedInitSandbox) Wait() error {
	if p.done != nil {
		<-p.done
	}
	return nil
}

func (p *pausedInitSandbox) serve(ctx context.Context, ls *link.Session) {
	for {
		st, err := ls.Accept(ctx)
		if err != nil {
			return
		}
		switch st.Kind() {
		case link.KindCtl:
			go p.serveCtl(ctx, st)
		case link.KindHolder:
			go p.serveHolder(st)
		default:
			_ = st.Close()
		}
	}
}

func (p *pausedInitSandbox) serveHolder(st *link.Stream) {
	conn, err := net.DialTimeout("unix", p.files.Sock, 3*time.Second)
	if err != nil {
		_ = st.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, st)
		if u, ok := conn.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(st, conn)
		_ = st.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = st.Close()
	_ = conn.Close()
}

func (p *pausedInitSandbox) serveCtl(ctx context.Context, st *link.Stream) {
	r := bufio.NewReader(st)
	w := proto.NewWriter(st)
	stopBeat := make(chan struct{})
	defer close(stopBeat)
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			_ = w.WriteJSON(proto.TEvent, proto.CtlEvent{Event: proto.EventHeartbeat})
			select {
			case <-tk.C:
			case <-stopBeat:
				return
			}
		}
	}()
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		if f.Type != proto.TRequest {
			continue
		}
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil {
			continue
		}
		switch req.Op {
		case proto.CtlStart:
			var a proto.CtlStartArgs
			_ = json.Unmarshal(req.Args, &a)
			pid, err := p.startHolder(ctx, a, w)
			resp := proto.Response{ID: req.ID, OK: err == nil}
			if err != nil {
				resp.Error = &proto.Error{Code: "wall_init", Message: err.Error(), Hint: "Resume the session."}
			} else {
				resp.Result, _ = json.Marshal(proto.CtlStartResult{AgentPID: pid, GuestABI: proto.GuestPlainTestV1})
			}
			_ = w.WriteJSON(proto.TResponse, resp)
		case proto.CtlStop:
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
			go func() { _ = p.Kill() }()
			return
		}
	}
}

func (p *pausedInitSandbox) startHolder(ctx context.Context, a proto.CtlStartArgs, ctlW *proto.Writer) (int, error) {
	if err := paths.EnsurePrivateDir(filepath.Dir(p.files.Sock)); err != nil {
		return 0, err
	}
	home := filepath.Join(p.root, host.GuestHome)
	if err := paths.EnsurePrivateDir(home); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema: 1, ID: a.Session, Name: a.Session, CreatedAt: now,
		Agent:  host.AgentInfo{Kind: a.Agent, Argv: a.Argv, Hooks: a.Hooks},
		Cwd:    a.Cwd,
		Env:    a.Env,
		Term:   host.TermInfo{Term: "xterm-256color", Cols: a.Cols, Rows: a.Rows},
		Status: host.Status{State: host.StateStarting, Since: now},
	}
	if err := m.SaveTo(p.files.Manifest); err != nil {
		return 0, err
	}
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		return 0, err
	}
	defer theirs.Close()
	if err := boundPausedEventSocket(ours, theirs); err != nil {
		_ = ours.Close()
		return 0, err
	}
	exe, err := os.Executable()
	if err != nil {
		_ = ours.Close()
		return 0, err
	}
	cmd := exec.CommandContext(ctx, exe, "holder", a.Session)
	cmd.Env = append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home}, p.files.Env()...)
	cmd.ExtraFiles = []*os.File{theirs}
	if err := cmd.Start(); err != nil {
		_ = ours.Close()
		return 0, err
	}
	hd := make(chan struct{})
	p.mu.Lock()
	p.holderCmd = cmd
	p.holderDone = hd
	p.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(hd)
	}()
	go p.readFD3(ours, ctlW)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		gm, err := host.LoadManifestFile(p.files.Manifest)
		if err == nil && gm.Run.AgentPID != 0 {
			return gm.Run.AgentPID, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, fmt.Errorf("guest holder did not start agent")
}

// boundPausedEventSocket makes queue pressure deliberate rather than relying
// on the OS's default socket buffers and thousands of manifest fsyncs.
func boundPausedEventSocket(reader net.Conn, writer *os.File) error {
	u, ok := reader.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the holder event reader is not a Unix socket")
	}
	if err := u.SetReadBuffer(4096); err != nil {
		return fmt.Errorf("bound the event reader's buffer: %w", err)
	}
	conn, err := net.FileConn(writer)
	if err != nil {
		return fmt.Errorf("open the event writer: %w", err)
	}
	defer conn.Close()
	u, ok = conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the holder event writer is not a Unix socket")
	}
	if err := u.SetWriteBuffer(4096); err != nil {
		return fmt.Errorf("bound the event writer's buffer: %w", err)
	}
	return nil
}

func (p *pausedInitSandbox) readFD3(conn net.Conn, ctlW *proto.Writer) {
	defer conn.Close()
	<-p.resumeFD3
	r := bufio.NewReader(conn)
	var head [4]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return
		}
		n := binary.LittleEndian.Uint32(head[:])
		if n == 0 || n > 64<<10 {
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		if proto.Type(buf[0]) == proto.TEvent {
			var ev proto.CtlEvent
			if json.Unmarshal(buf[1:], &ev) == nil && ev.Event == proto.EventEnded {
				p.sawEnded.Store(true)
			}
			_ = ctlW.Write(proto.TEvent, buf[1:])
		}
	}
}

// When the init end of fd 3 stalls and > 300 events overflow the guest
// holder's 256-slot queue before the agent exits, the terminal EventEnded is
// still delivered once reading resumes: ls shows the ended state as claimed,
// and the end alert is recorded.
func TestGuestHolderNeverDropsEndedWhenQueueOverflows(t *testing.T) {
	dir := testFolders(t)
	alertEvents := make(chan alerts.Event, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			alertEvents <- ev
		}
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "events = [\"exited\", \"failed\", \"needs_input\", \"started\"]\n" +
		"[[destination]]\ntype = \"webhook\"\nurl = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	const id = "endq2345"
	exitFlag := filepath.Join(dir, "exit-now")
	root := filepath.Join(dir, "guest-root")
	sb := &pausedInitSandbox{
		root:      root,
		files:     guest.Files(root),
		resumeFD3: make(chan struct{}),
	}
	old := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) { return sb, nil }
	t.Cleanup(func() {
		sandboxFor = old
		_ = sb.Kill()
	})

	t0 := time.Now().UTC()
	m := &host.Manifest{
		Schema: 1, ID: id, Name: "box", CreatedAt: t0, Cwd: dir,
		Agent: host.AgentInfo{
			Kind: "claude",
			Argv: []string{"/bin/sh", "-c", "while [ ! -f '" + exitFlag + "' ]; do sleep 0.02; done; exit 7"},
		},
		Env: []string{
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"HOME=" + filepath.Join(root, host.GuestHome),
			brand.Env("HOOK_SOCK") + "=" + sb.files.Hook,
			"TERM=xterm-256color",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: t0},
		Wall:   &host.WallRecord{Level: "none", Driver: driverPlain, Version: version.Version, At: t0},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, time.Second)
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
		}
	})

	waitFor(t, "the supervised session's start", func() bool {
		cur, err := host.LoadManifest(id)
		return err == nil && cur.Status.State == host.StateWorking
	})

	// Flood more than 300 events while fd 3's reader is paused. The fixture's
	// small socket buffers fill first, then all 256 ordinary queue slots.
	hookConn, err := net.DialTimeout("unix", sb.files.Hook, 3*time.Second)
	if err != nil {
		t.Fatalf("dial guest hook socket: %v", err)
	}
	defer hookConn.Close()
	_ = hookConn.SetDeadline(time.Now().Add(15 * time.Second))
	hw, hr := proto.NewWriter(hookConn), bufio.NewReader(hookConn)
	if err := hw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "hook"}); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Read(hr); err != nil {
		t.Fatal(err)
	}
	sendHook := func(seq uint64, ev host.HookEvent) {
		raw, _ := json.Marshal(ev)
		if err := hw.WriteJSON(proto.TRequest, proto.Request{ID: seq, Op: "hook", Args: raw}); err != nil {
			t.Fatalf("write hook #%d: %v", seq, err)
		}
		if _, err := proto.Read(hr); err != nil {
			t.Fatalf("read hook #%d reply: %v", seq, err)
		}
	}
	sendHook(1, host.HookEvent{Event: "notification", Payload: json.RawMessage(`{"message":"waiting"}`)})
	time.Sleep(1100 * time.Millisecond) // let Holder.tick queue status + needs_input alert too
	for i := range 600 {
		payload := fmt.Sprintf(`{"session_id":"3f2b8c1e-9d4a-4e6b-8f7c-%012d","source":"startup"}`, i+1)
		sendHook(uint64(i+2), host.HookEvent{Event: "session-start", Payload: json.RawMessage(payload)})
	}
	_ = hookConn.Close()

	// Trigger the agent's exit while fd 3 is still paused and g.out is full.
	if err := os.WriteFile(exitFlag, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "guest holder recording agent exit while fd 3 is paused", func() bool {
		gm, err := host.LoadManifestFile(sb.files.Manifest)
		return err == nil && gm.Status.ExitCode != nil && *gm.Status.ExitCode == 7
	})

	// Resume reading fd 3 and verify EventEnded arrives, ls shows the claimed
	// ended state, and the failed alert is recorded as claimed.
	close(sb.resumeFD3)

	waitFor(t, "EventEnded on fd 3", sb.sawEnded.Load)
	waitFor(t, "claimed ended state in host.List", func() bool {
		list, err := host.List()
		return err == nil && len(list) == 1 && list[0].State == host.StateErrored &&
			list[0].ExitCode != nil && *list[0].ExitCode == 7 && list[0].Claimed
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cl.Dial(ctx, bridgeHost(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.Sessions(ctx)
	if err != nil || len(rows) != 1 || !cl.Ended(rows[0]) || !rows[0].Claimed || rows[0].ExitCode == nil || *rows[0].ExitCode != 7 {
		t.Fatalf("client Sessions() = %+v, %v", rows, err)
	}

	var gotFailed *alerts.Event
	timeout := time.After(10 * time.Second)
	for gotFailed == nil {
		select {
		case ev := <-alertEvents:
			if ev.Event == alerts.Failed {
				gotFailed = &ev
			}
		case <-timeout:
			t.Fatal("timed out waiting for claimed failed alert")
		}
	}
	if !gotFailed.Claimed || gotFailed.ExitCode == nil || *gotFailed.ExitCode != 7 || !strings.HasSuffix(gotFailed.Summary, "(claimed)") {
		t.Fatalf("failed alert = %+v", *gotFailed)
	}
}
