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

// Run is the entry point of `<helper> wall-init` with no extra arguments.
func Run() error { return RunArgs(nil) }

// RunArgs is the entry point of `<helper> wall-init [args...]`. Inside a
// microvm (PID 1 or --self-test) it mounts the verified rootfs and serves over
// AF_VSOCK; otherwise it applies Landlock (when configured) and attaches fd 3.
func RunArgs(args []string) error {
	if handled, err := maybeRunMicroVM(args); handled {
		return err
	}
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

// RunOptions configures optional guest loopback services and microvm mode
// started by wall-init.
type RunOptions struct {
	Proxy              bool
	ProxyAddrs         []string
	DNS                bool
	DNSAddr            string
	OnDNSReady         func(udpAddr, tcpAddr net.Addr)
	Transparent        bool
	TransparentAddr    string
	OnTransparentReady func(net.Addr)
	MicroVM            bool
	ReportMem          bool
	AgentCgroupDir     string
	AgentsBinDir       string
	Sudo               bool
	Docker             bool
	DockerGID          int
	DockerdPaths       []string
}

type managedHolder struct {
	id       string
	sock     string
	cwd      string
	env      []string
	agentPID int
	cmd      *exec.Cmd
	done     chan struct{}
}

type runner struct {
	runDir string
	opts   RunOptions
	sess   *link.Session

	mu           sync.Mutex
	ctlW         *proto.Writer
	holders      map[string]*managedHolder
	managed      map[int]struct{}
	baseEnv      []string
	baseCwd      string
	keepFiles    []string
	execs        map[string]*pendingExec
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
	if opts.MicroVM {
		if err := chownMicroVMDir(runDir, 0, 0, 0o755); err != nil {
			_ = conn.Close()
			return err
		}
	} else if err := paths.EnsurePrivateDir(runDir); err != nil {
		_ = conn.Close()
		return err
	}
	_ = writeBrowserHelper(runDir)
	sess := link.New(conn, link.Guest, link.Options{})
	r := &runner{
		runDir:       runDir,
		opts:         opts,
		sess:         sess,
		holders:      map[string]*managedHolder{},
		managed:      map[int]struct{}{},
		execs:        map[string]*pendingExec{},
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
	case link.KindExec:
		r.serveExec(ctx, st)
	default:
		r.handleExtraStream(ctx, st)
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
		if r.opts.MicroVM || r.opts.ReportMem {
			r.emitMemClaim()
		}
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
				if r.opts.MicroVM || r.opts.ReportMem {
					r.emitMemClaim()
				}
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
		startReplied := make(chan struct{})
		res, perr := r.startHolder(ctx, a, startReplied)
		reply(res, perr)
		close(startReplied)
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
		r.handleMoreCtl(ctx, reply, req)
	}
}

func (r *runner) startHolder(ctx context.Context, a StartReq, startReplied <-chan struct{}) (*StartResp, *proto.Error) {
	if err := ctx.Err(); err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Try again."}
	}
	if a.Session == "" {
		return nil, &proto.Error{Code: "bad_request", Message: "missing session id", Hint: "Pass a session id in start."}
	}
	if r.opts.Docker && findDockerd(r.opts.DockerdPaths...) == "" {
		return nil, &proto.Error{
			Code:    "docker_missing",
			Message: "this microvm image has no dockerd",
			Hint:    "Pass --image with an image that includes Docker, or omit --docker.",
		}
	}
	exe := os.Getenv(brand.Env("GUEST_HELPER"))
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check the host helper installation."}
		}
	}
	r.mu.Lock()
	first := len(r.holders) == 0
	r.mu.Unlock()

	sockPath, hookPath, manifestPath, logPath, err := r.holderPaths(a.Session, first)
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check /run permissions inside the sandbox."}
	}

	agentEnv, perr := r.prepareSessionEnv(a, first)
	if perr != nil {
		return nil, perr
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
	startCfg := host.GuestStartConfig{
		Manifest:  m,
		SecretEnv: a.Secrets,
		Notes:     a.Notes,
	}
	if r.opts.MicroVM && os.Geteuid() == 0 {
		startCfg.AgentUID = GuestAgentUID
		startCfg.AgentGID = GuestAgentGID
		startCfg.AgentGroups = r.agentGroups()
		startCfg.HolderUID = GuestHolderUID
		startCfg.HolderGID = GuestHolderGID
		startCfg.HookGID = GuestAgentGID
		startCfg.AgentCgroup = r.agentCgroupDir()
	}
	cfgBytes, err := json.Marshal(startCfg)
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Retry starting the session."}
	}

	evR, evW, err := os.Pipe()
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check file descriptor limits on the host."}
	}

	//nolint:gosec // G702: exe is set by guest init itself (/run/<brand>/tools/init or os.Executable)
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

	mh := &managedHolder{id: a.Session, sock: sockPath, cwd: a.Cwd, env: append([]string(nil), agentEnv...), cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		r.mu.Lock()
		delete(r.managed, cmd.Process.Pid)
		r.mu.Unlock()
		close(mh.done)
	}()

	started := make(chan host.GuestEvent, 1)
	go r.readHolderEvents(ctx, a.Session, evR, started, startReplied)

	select {
	case ev, ok := <-started:
		if !ok {
			return nil, &proto.Error{Code: "start_failed", Message: "the guest holder exited before starting the agent", Hint: "Check the session logs."}
		}
		if ev.Type == "start_failed" {
			msg := ev.Reason
			if msg == "" {
				msg = "the guest holder could not start the agent"
			}
			return nil, &proto.Error{Code: "start_failed", Message: msg, Hint: "Check the session command and logs."}
		}
		r.mu.Lock()
		mh.agentPID = ev.AgentPID
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
