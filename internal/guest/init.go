package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// HeartbeatInterval is how often guest init emits a heartbeat event on ctl.
var HeartbeatInterval = 5 * time.Second

// linkFD is the file descriptor inherited from the supervisor's socket pair.
const linkFD = 3

// Run is the entry point of `<helper> wall-init`. When Landlock configuration is
// present in the environment, it applies Landlock as its very first act before
// attaching to fd 3. It then serves ctl, holder, put and (when enabled) guest
// loopback proxy streams until stopped or the link ends.
func Run() error {
	if err := applyWallLandlock(); err != nil {
		return err
	}
	f := os.NewFile(linkFD, "link")
	if f == nil {
		return errors.New("guest: no link socket on fd 3. Try: start sessions through the supervisor")
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("guest: attach to link fd 3: %w. Try: start sessions through the supervisor", err)
	}
	runDir := os.Getenv(brand.Env("GUEST_RUN_DIR"))
	if runDir == "" {
		runDir = filepath.Join("/run", brand.Name)
	}
	opts := RunOptions{
		Proxy:   os.Getenv(brand.Env("GUEST_PROXY")) == "1",
		DNS:     os.Getenv(brand.Env("GUEST_DNS")) == "1",
		DNSAddr: os.Getenv(brand.Env("GUEST_DNS_ADDR")),
	}
	_ = os.Unsetenv(brand.Env("GUEST_PROXY"))
	_ = os.Unsetenv(brand.Env("GUEST_DNS"))
	_ = os.Unsetenv(brand.Env("GUEST_DNS_ADDR"))
	return RunConnWithOptions(conn, runDir, opts)
}

// RunOptions configures optional guest loopback services started by wall-init.
type RunOptions struct {
	Proxy      bool
	ProxyAddrs []string
	DNS        bool
	DNSAddr    string
	OnDNSReady func(udpAddr, tcpAddr net.Addr)
}

type managedHolder struct {
	id   string
	sock string
	cmd  *exec.Cmd
	done chan struct{}
}

type runner struct {
	runDir string
	sess   *link.Session

	mu           sync.Mutex
	ctlW         *proto.Writer
	holders      map[string]*managedHolder
	managed      map[int]struct{}
	shutdownOnce sync.Once
	shutdownDone chan struct{}

	stopOnce sync.Once
	stopCh   chan struct{}
}

// RunConn runs guest init over conn using runDir for local guest sockets,
// manifests, logs and uploaded paste files.
func RunConn(conn net.Conn, runDir string) error {
	return RunConnWithOptions(conn, runDir, RunOptions{})
}

// RunConnWithOptions runs guest init over conn with optional guest loopback
// proxy forwarding over KindEgress link streams.
func RunConnWithOptions(conn net.Conn, runDir string, opts RunOptions) error {
	if err := paths.EnsurePrivateDir(runDir); err != nil {
		_ = conn.Close()
		return err
	}
	sess := link.New(conn, link.Guest, link.Options{})
	r := &runner{
		runDir:       runDir,
		sess:         sess,
		holders:      map[string]*managedHolder{},
		managed:      map[int]struct{}{},
		shutdownDone: make(chan struct{}),
		stopCh:       make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	svc, err := startLoopbackServices(ctx, r, opts, &wg)
	if err != nil {
		_ = sess.Close()
		return err
	}

	reapStop := startReaper(r.isManaged)
	defer reapStop()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)

	go func() {
		select {
		case <-sig:
			r.shutdown(3 * time.Second)
			cancel()
		case <-r.stopCh:
			cancel()
		case <-sess.Done():
			r.shutdown(3 * time.Second)
			cancel()
		}
	}()

	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			break
		}
		wg.Add(1)
		go func(st *link.Stream) {
			defer wg.Done()
			r.handleStream(ctx, st)
		}(st)
	}
	r.shutdown(3 * time.Second)
	cancel()
	svc.close()
	_ = sess.Close()
	wg.Wait()
	return nil
}

// OpenEgress implements StreamOpener for guest.Proxy by opening a KindEgress
// stream over the guest-to-supervisor link.
func (r *runner) OpenEgress(ctx context.Context, hdr egress.StreamHeader) (*link.Stream, error) {
	return r.sess.Open(ctx, link.KindEgress, hdr)
}

// OpenDNS implements DNSOpener for guest.DNSStub by opening a KindDNS stream
// over the guest-to-supervisor link.
func (r *runner) OpenDNS(ctx context.Context) (*link.Stream, error) {
	return r.sess.Open(ctx, link.KindDNS, nil)
}

func (r *runner) isManaged(pid int) bool {
	r.mu.Lock()
	_, ok := r.managed[pid]
	r.mu.Unlock()
	return ok
}

func (r *runner) handleStream(ctx context.Context, st *link.Stream) {
	switch st.Kind() {
	case link.KindCtl:
		r.serveCtl(ctx, st)
	case link.KindHolder:
		r.serveHolder(ctx, st)
	case link.KindPut:
		r.servePut(st)
	case link.KindPort:
		r.servePort(ctx, st)
	default:
		_ = st.Close()
	}
}

func (r *runner) serveCtl(ctx context.Context, st *link.Stream) {
	defer st.Close()
	w := proto.NewWriter(st)
	r.mu.Lock()
	r.ctlW = w
	r.mu.Unlock()

	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		interval := HeartbeatInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.sendEvent(Event{Type: "heartbeat"})
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			}
		}
	}()
	defer func() {
		r.signalStop()
		<-hbDone
	}()

	rd := bufio.NewReaderSize(st, 64<<10)
	for {
		f, err := proto.Read(rd)
		if err != nil {
			return
		}
		if f.Type != proto.TRequest {
			continue
		}
		var req proto.Request
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			continue
		}
		r.handleCtlRequest(ctx, w, req)
	}
}

func (r *runner) sendEvent(ev Event) {
	r.mu.Lock()
	w := r.ctlW
	r.mu.Unlock()
	if w == nil {
		return
	}
	_ = w.WriteJSON(proto.TEvent, ev)
}

func (r *runner) handleCtlRequest(ctx context.Context, w *proto.Writer, req proto.Request) {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	switch req.Op {
	case "start":
		var a StartReq
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, &proto.Error{Code: "bad_request", Message: "invalid start request", Hint: "Update the host helper."})
			return
		}
		res, perr := r.startHolder(ctx, a)
		reply(res, perr)
	case "stop":
		var a StopReq
		_ = json.Unmarshal(req.Args, &a)
		grace := time.Duration(a.GraceMS) * time.Millisecond
		if grace <= 0 {
			grace = 5 * time.Second
		}
		// Claim shutdownOnce synchronously with the requested grace before
		// replying so a subsequent SIGTERM cannot shorten the grace period.
		r.beginShutdown(grace)
		reply(map[string]any{}, nil)
	default:
		reply(nil, &proto.Error{Code: "unknown_op", Message: "unknown ctl op " + req.Op, Hint: "Update the host helper."})
	}
}

func (r *runner) startHolder(ctx context.Context, a StartReq) (*StartResp, *proto.Error) {
	if err := ctx.Err(); err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Try again."}
	}
	if a.Session == "" {
		return nil, &proto.Error{Code: "bad_request", Message: "missing session id", Hint: "Pass a session id in start."}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check the host helper installation."}
	}
	r.mu.Lock()
	first := len(r.holders) == 0
	r.mu.Unlock()

	sockName, hookName := "holder-"+a.Session+".sock", "hook-"+a.Session+".sock"
	if first {
		sockName, hookName = "holder.sock", "hook.sock"
	}
	sockPath := filepath.Join(r.runDir, sockName)
	hookPath := filepath.Join(r.runDir, hookName)
	manifestPath := filepath.Join(r.runDir, "manifest-"+a.Session+".json")
	logPath := filepath.Join(r.runDir, "holder-"+a.Session+".log")

	agentEnv, err := applyStartPackageSettings(a, a.Env)
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check sandbox.package_managers and network.min_release_age."}
	}
	agentEnv = withEnvVar(agentEnv, brand.Env("HOOK_SOCK"), hookPath)
	agentEnv = withEnvVar(agentEnv, brand.Env("SESSION"), a.Session)

	m := host.Manifest{
		Schema:  1,
		ID:      a.Session,
		Name:    a.Name,
		Agent:   host.AgentInfo{Kind: a.Agent, Argv: a.Argv, AgentSessionID: a.AgentID, IDSource: a.IDSource},
		Cwd:     a.Cwd,
		Env:     agentEnv,
		Term:    host.TermInfo{Term: a.Term, Cols: a.Cols, Rows: a.Rows},
		RunHook: a.RunHook,
	}
	cfgBytes, err := json.Marshal(host.GuestStartConfig{
		Manifest:  m,
		SecretEnv: a.Secrets,
		Notes:     a.Notes,
	})
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Retry starting the session."}
	}

	evR, evW, err := os.Pipe()
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check file descriptor limits on the host."}
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, "holder", a.Session)
	cmd.Stdin = bytes.NewReader(cfgBytes)
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if devnull != nil {
		cmd.Stdout, cmd.Stderr = devnull, devnull
	}
	cmd.ExtraFiles = []*os.File{evW}
	cmd.Env = guestHolderEnv(sockPath, hookPath, manifestPath, logPath)

	r.mu.Lock()
	err = cmd.Start()
	if err == nil {
		r.managed[cmd.Process.Pid] = struct{}{}
	}
	r.mu.Unlock()
	_ = evW.Close()
	if devnull != nil {
		_ = devnull.Close()
	}
	if err != nil {
		_ = evR.Close()
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check the host helper binary."}
	}

	mh := &managedHolder{id: a.Session, sock: sockPath, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		r.mu.Lock()
		delete(r.managed, cmd.Process.Pid)
		r.mu.Unlock()
		close(mh.done)
	}()

	started := make(chan host.GuestEvent, 1)
	go r.readHolderEvents(a.Session, evR, started)

	select {
	case ev, ok := <-started:
		if !ok {
			return nil, &proto.Error{Code: "start_failed", Message: "the guest holder exited before starting the agent", Hint: "Check the session logs."}
		}
		if ev.Type == "start_failed" {
			return nil, &proto.Error{Code: "start_failed", Message: ev.Reason, Hint: "Check the session command and logs."}
		}
		r.mu.Lock()
		r.holders[a.Session] = mh
		r.mu.Unlock()
		return &StartResp{HolderPID: ev.HolderPID, AgentPID: ev.AgentPID, Cols: ev.Cols, Rows: ev.Rows}, nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, &proto.Error{Code: "start_failed", Message: ctx.Err().Error(), Hint: "Try again."}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return nil, &proto.Error{Code: "start_failed", Message: "the guest holder did not start in time", Hint: "Try again."}
	}
}

func (r *runner) readHolderEvents(sessionID string, evR *os.File, started chan<- host.GuestEvent) {
	defer evR.Close()
	sentStart := false
	defer func() {
		if !sentStart {
			close(started)
		}
	}()
	sc := bufio.NewScanner(evR)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		var ge host.GuestEvent
		if json.Unmarshal(sc.Bytes(), &ge) != nil {
			continue
		}
		if ge.Session == "" {
			ge.Session = sessionID
		}
		if !sentStart && (ge.Type == "started" || ge.Type == "start_failed") {
			sentStart = true
			started <- ge
			close(started)
			continue
		}
		r.sendEvent(Event{
			Type:       ge.Type,
			Session:    ge.Session,
			State:      ge.State,
			Source:     ge.Source,
			Reason:     ge.Reason,
			FullScreen: ge.FullScreen,
			Kind:       ge.Kind,
			Detail:     ge.Detail,
			TurnNS:     ge.TurnNS,
			AgentID:    ge.AgentID,
			IDSource:   ge.IDSource,
			ExitCode:   ge.ExitCode,
			Signal:     ge.Signal,
			Cols:       ge.Cols,
			Rows:       ge.Rows,
		})
	}
}

func (r *runner) signalStop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// beginShutdown claims shutdownOnce synchronously with grace and runs holder
// termination asynchronously so callers can reply before holders finish exiting
// without racing a later SIGTERM handler.
func (r *runner) beginShutdown(grace time.Duration) {
	r.shutdownOnce.Do(func() {
		go func() {
			defer close(r.shutdownDone)
			r.stopHolders(grace)
			r.signalStop()
		}()
	})
}

func (r *runner) shutdown(grace time.Duration) {
	r.beginShutdown(grace)
	<-r.shutdownDone
}

func (r *runner) stopHolders(grace time.Duration) {
	r.mu.Lock()
	list := make([]*managedHolder, 0, len(r.holders))
	for _, mh := range r.holders {
		list = append(list, mh)
	}
	r.mu.Unlock()

	for _, mh := range list {
		if mh.cmd != nil && mh.cmd.Process != nil {
			_ = mh.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	for _, mh := range list {
		select {
		case <-mh.done:
		case <-deadline.C:
			if mh.cmd != nil && mh.cmd.Process != nil {
				_ = mh.cmd.Process.Kill()
			}
			<-mh.done
		}
	}
}
