package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Options tunes supervisor intervals, mostly for fast tests. Zero fields use
// their spec defaults.
type Options struct {
	HeartbeatTimeout time.Duration // spec 10.3: 15s
	TickInterval     time.Duration // 1s
	SecretsWait      time.Duration // 30s
	StopGrace        time.Duration // 5s
	UploadTimeout    time.Duration // 15s
	AlertsPath       string        // override alerts config path (optional)
}

func (o Options) withDefaults() Options {
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = 15 * time.Second
	}
	if o.TickInterval <= 0 {
		o.TickInterval = time.Second
	}
	if o.SecretsWait <= 0 {
		o.SecretsWait = 30 * time.Second
	}
	if o.StopGrace <= 0 {
		o.StopGrace = 5 * time.Second
	}
	return o
}

// Supervisor holds one walled session's trusted state.
type Supervisor struct {
	id        string
	opt       Options
	log       *slog.Logger
	audit     *auditLog
	ln        net.Listener
	peerCheck bool
	sb        Sandbox
	link      *link.Session
	ctl       *link.Stream
	ctlW      *proto.Writer
	limiter   *eventLimiter
	alerts    *alerts.Dispatcher

	ctlMu   sync.Mutex
	ctlNext atomic.Uint64
	pending map[uint64]chan proto.Response

	mu       sync.Mutex
	m        *host.Manifest
	mem      host.MemTracker
	dirty    bool
	ended    bool
	stopping bool
	lastHB   time.Time
	hbSilent bool

	saveMu sync.Mutex
	closed bool

	wg     sync.WaitGroup
	connMu sync.Mutex
	conns  map[net.Conn]struct{}

	stopOnce   sync.Once
	stopCh     chan stopRequest
	doneCh     chan struct{}
	ctlDone    chan struct{}
	acceptDone chan struct{}
}

type stopRequest struct {
	reason string
	system bool
}

// Run supervises session id using the test-only plain driver. It returns when
// the sandbox has ended and the session is stopped, ended or killed.
func Run(id string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate host helper: %w", err)
	}
	sb := NewPlainSandbox(id, exe, paths.GuestRuntimeDir(id))
	return RunWith(id, sb, Options{})
}

// RunWith supervises session id over sb. Tests pass mock or in-process
// sandboxes here.
func RunWith(id string, sb Sandbox, opt Options) error {
	opt = opt.withDefaults()
	m, err := host.LoadManifest(id)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	lf, err := host.OpenRotatingLog(paths.SupervisorLog(id))
	if err != nil {
		return err
	}
	defer lf.Close()
	aud, err := openAuditLog(paths.AuditLog(id))
	if err != nil {
		return err
	}
	defer aud.Close()

	s := &Supervisor{
		id:         id,
		opt:        opt,
		log:        host.NewSessionLogger(lf),
		audit:      aud,
		sb:         sb,
		m:          m,
		limiter:    newEventLimiter(MaxCtlEventsPerSec),
		pending:    map[uint64]chan proto.Response{},
		conns:      map[net.Conn]struct{}{},
		lastHB:     time.Now(),
		stopCh:     make(chan stopRequest, 1),
		doneCh:     make(chan struct{}),
		ctlDone:    make(chan struct{}),
		acceptDone: make(chan struct{}),
	}
	s.log.Info("supervisor starting", "session", id, "version", version.Version)

	if err := s.listen(); err != nil {
		s.log.Error("listen failed", "err", err)
		return err
	}
	defer s.closeSocket()

	restored := host.WasRestored(m)
	secretEnv, notes := s.secretsBeforeStart(restored)

	if err := s.bootSandbox(secretEnv, notes, restored); err != nil {
		s.log.Error("sandbox start failed", "err", err)
		code := 127
		reason := api.CleanText(err.Error(), maxDetailRunes)
		s.mu.Lock()
		s.m.Status = host.Status{State: host.StateErrored, Since: time.Now().UTC(), ExitCode: &code, Reason: reason}
		mCopy := *s.m
		s.mu.Unlock()
		_ = s.save(&mCopy, true)
		_ = s.sb.Kill()
		_ = s.sb.Destroy()
		s.wg.Wait()
		return err
	}

	go s.acceptLoop()
	s.wg.Add(1)
	go s.tickLoop()

	sbExit := make(chan Exit, 1)
	go func() { sbExit <- s.sb.Wait() }()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)

	select {
	case sr := <-s.stopCh:
		s.finishStop(sr.reason, sr.system)
	case sigVal := <-sig:
		s.log.Info("stopping on signal", "signal", sigVal.String())
		s.finishStop("stopped with the host", true)
	case ex := <-sbExit:
		s.finishSandboxExit(ex)
	case <-s.link.Done():
		s.finishLinkClosed()
	}
	return nil
}

func (s *Supervisor) listen() error {
	sock := paths.Socket(s.id)
	for _, dir := range []string{filepath.Dir(filepath.Dir(sock)), filepath.Dir(sock)} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	ln, err := host.ListenPrivate(sock, "the session's socket")
	if err != nil {
		return err
	}
	s.ln = ln
	s.peerCheck = host.InOwnUnit(s.id)
	s.log.Info("session socket open", "peer_check", s.peerCheck)
	return nil
}

func (s *Supervisor) closeSocket() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	_ = os.Remove(paths.Socket(s.id))
}

func (s *Supervisor) bootSandbox(secretEnv, notes []string, restored bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Initialise unit memory tracking before starting ctlReadLoop so a fast
	// exit event cannot race with MemTracker.Init.
	s.mu.Lock()
	s.mem.Init(s.id)
	s.mu.Unlock()

	conn, err := s.sb.Start(ctx)
	if err != nil {
		return err
	}
	s.link = link.New(conn, link.Supervisor, link.Options{})
	ctl, err := s.link.Open(ctx, link.KindCtl, nil)
	if err != nil {
		_ = s.link.Close()
		return fmt.Errorf("open ctl stream: %w", err)
	}
	s.ctl = ctl
	s.ctlW = proto.NewWriter(ctl)
	// Start the alert dispatcher before ctlReadLoop so fast agent exits never
	// miss their alert.
	s.initAlerts()
	s.wg.Add(1)
	go s.ctlReadLoop()

	startReq := guest.StartReq{
		Session:  s.id,
		Name:     s.m.Name,
		Agent:    s.m.Agent.Kind,
		Argv:     s.m.Agent.Argv,
		Env:      s.m.Env,
		Secrets:  secretEnv,
		Cwd:      s.m.Cwd,
		Term:     s.m.Term.Term,
		Cols:     s.m.Term.Cols,
		Rows:     s.m.Term.Rows,
		RunHook:  s.m.RunHook,
		Notes:    notes,
		AgentID:  s.m.Agent.AgentSessionID,
		IDSource: s.m.Agent.IDSource,
	}
	resp, err := s.ctlCall(ctx, "start", startReq)
	// Clear in-memory secret copies as soon as start has been sent.
	clear(secretEnv)
	clear(startReq.Secrets)
	if err != nil {
		s.closeAlerts()
		_ = s.link.Close()
		return err
	}
	if !resp.OK {
		s.closeAlerts()
		_ = s.link.Close()
		ce := cleanGuestError(resp.Error, "start_failed", "guest start failed", "Check the session logs.")
		return errors.New(ce.Message)
	}
	var sr guest.StartResp
	_ = json.Unmarshal(resp.Result, &sr)

	now := time.Now().UTC()
	s.mu.Lock()
	s.m.RunHook = ""
	if u := host.OwnUnit(s.id); u != "" {
		s.m.Run.Unit = u
	}
	s.m.Run.HolderPID = os.Getpid()
	s.m.Run.AgentPID = sr.AgentPID
	s.m.Run.Socket = paths.Socket(s.id)
	s.m.Run.BootID = host.BootID()
	s.m.Run.StartedAt = now
	if exe, err := os.Executable(); err == nil {
		s.m.HolderBinary = exe
	}
	s.m.HostVersion = version.Version
	if sr.Cols > 0 && sr.Rows > 0 {
		s.m.Term.Cols, s.m.Term.Rows = sr.Cols, sr.Rows
	}
	// Only set initial working status if an early guest status/ended claim has
	// not already arrived before start returned.
	if !s.ended && s.m.Guest == nil {
		s.m.Status = host.Status{State: host.StateWorking, Since: now, Source: "start"}
	}
	s.lastHB = time.Now()
	mCopy := *s.m
	wallLvl, wallDrv := "", ""
	if s.m.Wall != nil {
		wallLvl, wallDrv = s.m.Wall.Level, s.m.Wall.Driver
	}
	s.mu.Unlock()

	if err := s.save(&mCopy, false); err != nil {
		s.log.Warn("manifest not saved", "err", err)
	}
	s.notifyStarted(restored)
	s.audit.Record(AuditEntry{Event: "start", Session: s.id, Wall: wallLvl, Driver: wallDrv})
	return nil
}

func (s *Supervisor) ctlCall(ctx context.Context, op string, args any) (proto.Response, error) {
	id := s.ctlNext.Add(1)
	ch := make(chan proto.Response, 1)
	s.ctlMu.Lock()
	s.pending[id] = ch
	s.ctlMu.Unlock()
	defer func() {
		s.ctlMu.Lock()
		delete(s.pending, id)
		s.ctlMu.Unlock()
	}()

	req := proto.Request{ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return proto.Response{}, fmt.Errorf("encode ctl %s: %w", op, err)
		}
		req.Args = b
	}
	if err := s.ctlW.WriteJSONCtx(ctx, proto.TRequest, req); err != nil {
		return proto.Response{}, fmt.Errorf("send ctl %s: %w", op, err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-s.ctlDone:
		return proto.Response{}, errors.New("ctl stream closed")
	case <-ctx.Done():
		return proto.Response{}, ctx.Err()
	}
}

func (s *Supervisor) ctlReadLoop() {
	defer s.wg.Done()
	defer close(s.ctlDone)
	r := bufio.NewReaderSize(s.ctl, 64<<10)
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TResponse:
			if len(f.Payload) > MaxCtlEventBytes {
				s.refuseFrame("guest-ctl", f.Type)
				_ = s.link.Close()
				return
			}
			var resp proto.Response
			if json.Unmarshal(f.Payload, &resp) != nil {
				continue
			}
			s.ctlMu.Lock()
			ch := s.pending[resp.ID]
			s.ctlMu.Unlock()
			if ch != nil {
				select {
				case ch <- resp:
				default:
				}
			}
		case proto.TEvent:
			if !s.limiter.Allow(time.Now()) {
				continue
			}
			ev, err := ParseEvent(f.Payload)
			if err != nil {
				s.log.Warn("dropped invalid ctl event", "err", err)
				continue
			}
			s.applyGuestEvent(ev)
		default:
			s.refuseFrame("guest-ctl", f.Type)
			_ = s.link.Close()
			return
		}
	}
}

func (s *Supervisor) acceptLoop() {
	defer close(s.acceptDone)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		if !s.trackConn(conn) {
			continue
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer s.untrackConn(c)
			s.serveConn(c)
		}(conn)
	}
}

func (s *Supervisor) trackConn(c net.Conn) bool {
	s.connMu.Lock()
	if s.conns == nil {
		s.connMu.Unlock()
		_ = c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	s.connMu.Unlock()
	return true
}

func (s *Supervisor) untrackConn(c net.Conn) {
	s.connMu.Lock()
	delete(s.conns, c)
	s.connMu.Unlock()
}

func (s *Supervisor) closeAllConns() {
	s.connMu.Lock()
	conns := s.conns
	s.conns = nil
	s.connMu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
}
