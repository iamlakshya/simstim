package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
	"github.com/iamlakshya/simstim/internal/version"
)

// RingSize is how much raw output each session keeps for replay.
const RingSize = 8 << 20

// clientQueue is how many output frames may wait for a slow client
// before it is dropped. A dropped client reconnects and catches up by offset.
const clientQueue = 4096

type outFrame struct {
	t proto.Type
	p []byte
}

type hclient struct {
	conn     net.Conn
	w        *proto.Writer
	out      chan outFrame
	attached bool
	once     sync.Once
	done     chan struct{}
	tr       clientTrace // the debug trace of this attach (holder_trace.go)
}

func (c *hclient) close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// Holder owns one session: its PTY, output ring, socket and manifest.
type Holder struct {
	id   string
	log  *slog.Logger
	ln   net.Listener
	ptmx *os.File
	fd   int
	cmd  *exec.Cmd

	mu        sync.Mutex
	m         *Manifest
	ring      *ring.Ring
	clients   map[*hclient]struct{}
	leader    *hclient
	scan      scanner
	lastOut   time.Time
	needInput bool
	busy      bool
	exited    bool
	ended     proto.Ended
	dirty     bool
	// The memory cap's kills (holder_oom.go): the count accounted for, the
	// count the last tick read, and whether cmd.Wait has returned.
	oomSeen  int64
	oomNoted int64
	reaped   bool
	// The warning before the memory cap (holder_mem.go).
	mem memWatch
	// stopping is true while the system stops the session (for example at shutdown).
	stopping bool
	// saveMu orders the manifest's saves; closed is set by the last one.
	saveMu sync.Mutex
	closed bool
	// From agent hooks.
	hookSeen    bool
	hookBusy    bool
	lastTurnEnd time.Time
	notifyMsg   string
	// readDone is closed when readLoop has read all the agent's output.
	readDone chan struct{}
	// The shadow terminal, for snapshots and query answers; nil if unavailable.
	vt     *VT
	ptyOut chan []byte // ordered writes to the PTY from the shadow terminal
	// From screen rules.
	scr       *screenModel
	ruleState string
	ruleID    string
	// Alerts to the user's own destinations (alerts.go).
	al holderAlerts
	// Before the agent starts: its secrets (secrets.go), and notes to show in
	// its terminal first (start.go).
	secretEnv []string
	notes     []string
	// The debug trace (holder_trace.go), on while a traced client is attached.
	dbg holderDebug
}

// RunHolder runs the holder for session id until the session is killed.
func RunHolder(id string) error {
	m, err := LoadManifest(id)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	lf, err := os.OpenFile(paths.HolderLog(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open the holder log: %w", err)
	}
	defer lf.Close()
	h := &Holder{
		id:       id,
		m:        m,
		log:      newHolderLog(lf),
		ring:     ring.New(RingSize),
		clients:  map[*hclient]struct{}{},
		readDone: make(chan struct{}),
	}
	h.log.Info("holder starting", "session", id, "helper", brand.HostBinary, "version", version.Version)
	restored := wasRestored(m)
	if err := h.listen(); err != nil {
		h.log.Error("listen failed", "err", err)
		return err
	}
	h.secretsBeforeStart(restored)
	if err := h.start(); err != nil {
		h.log.Error("agent did not start", "err", err)
		h.mu.Lock()
		code := 127
		h.m.Status = Status{State: StateErrored, Since: time.Now().UTC(), ExitCode: &code, Reason: err.Error()}
		h.mu.Unlock()
		_ = h.m.Save()
		h.ln.Close()
		os.Remove(paths.Socket(id))
		return err
	}
	h.startAlerts(restored)
	h.startVT()
	go h.acceptLoop()
	go h.readLoop()
	go h.waitLoop()
	go h.tick()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	s := <-sig
	h.log.Info("stopping on a signal", "signal", s.String())
	// A signal means the system (or the user's manager) is stopping the
	// session, for example at shutdown. The agent did not end by itself, so the
	// session stays eligible for restore after the restart.
	h.stop("stopped with the host", true)
	return nil
}

// newHolderLog returns the logger of a holder's log file: one line for each
// event, with a short message and its details as key=value text. It never
// gets a secret's value or the agent's output.
func newHolderLog(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}

func (h *Holder) listen() error {
	sock := paths.Socket(h.id)
	if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(filepath.Dir(sock))); err != nil {
		return err
	}
	_ = os.Remove(sock)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		return fmt.Errorf("listen on the session's socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("make the session's socket private: %w", err)
	}
	h.ln = ln
	return nil
}

func (h *Holder) start() error {
	m := h.m
	if len(m.Agent.Argv) == 0 {
		return errors.New("no command to run")
	}
	// The setup (or resume) hook and any notes show in the session's
	// terminal before the agent (start.go).
	argv := startArgv(m.Agent.Argv, m.RunHook, hookVars(m, wasRestored(m)), h.notes)
	m.RunHook = ""
	bin, err := lookPath(argv[0], envValue(m.Env, "PATH"))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), bin, argv[1:]...)
	cmd.Args[0] = argv[0]
	// Secrets go into the agent's environment and nowhere else.
	cmd.Env = append(append([]string(nil), m.Env...), h.secretEnv...)
	cmd.Dir = m.Cwd
	cols, rows := m.Term.Cols, m.Term.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: clamp16(cols), Rows: clamp16(rows)})
	cmd.Env, h.secretEnv = nil, nil // the agent has its copy; the holder keeps none
	if err != nil {
		return fmt.Errorf("start %s in a terminal: %w", argv[0], err)
	}
	h.ptmx = ptmx
	h.fd = int(ptmx.Fd())
	if _, ok := agents.Find(m.Agent.Kind); ok {
		h.scr = newScreenModel(cols, rows)
	}
	h.cmd = cmd
	// Make the agent, not the holder, the first choice of the OOM killer.
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("500"), 0)
	h.oomSeen = readOOMKills(h.id)
	h.oomNoted = h.oomSeen
	h.startMemory()

	h.mu.Lock()
	now := time.Now().UTC()
	if u := ownUnit(h.id); u != "" {
		m.Run.Unit = u
	}
	m.Run.HolderPID = os.Getpid()
	m.Run.AgentPID = cmd.Process.Pid
	m.Run.Socket = paths.Socket(h.id)
	m.Run.BootID = BootID()
	m.Run.StartedAt = now
	if exe, err := os.Executable(); err == nil {
		m.HolderBinary = exe
	}
	m.HostVersion = version.Version
	m.Status = Status{State: StateWorking, Since: now, Source: "start"}
	h.lastOut = time.Now()
	h.mu.Unlock()
	if err := m.Save(); err != nil {
		h.log.Warn("manifest not saved", "err", err)
	}
	h.log.Info("agent started", "argv", m.Agent.Argv, "pid", cmd.Process.Pid)
	return nil
}

func (h *Holder) readLoop() {
	defer close(h.readDone)
	buf := make([]byte, 64<<10)
	for {
		n, err := h.ptmx.Read(buf)
		if n > 0 {
			h.output(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) {
				h.log.Warn("pty read failed", "err", err)
			}
			return
		}
	}
}

func (h *Holder) output(data []byte) {
	lt := h.lockTimed()
	off := h.ring.End()
	h.ring.Write(data)
	if h.vt != nil {
		h.vt.Feed(off, data)
	}
	payload := proto.EncodeOutput(off, data)
	for c := range h.clients {
		if !c.attached {
			continue
		}
		select {
		case c.out <- outFrame{proto.TOutput, payload}:
		default:
			h.log.Warn("client too slow, dropping it")
			go c.close()
		}
	}
	h.lastOut = time.Now()
	h.m.LastActivityAt = time.Now().UTC()
	fed := lt.now()
	h.scr.feed(data)
	feed := lt.since(fed)
	h.scan.feed(data, func(s outSignal) {
		switch s {
		case sigTitle:
			if h.scr != nil {
				h.scr.title = h.scan.title
				h.scr.dirty = true
			}
		case sigBell, sigNotify:
			h.needInput = true
		case sigProgressBusy:
			h.busy = true
		case sigProgressDone:
			h.busy = false
		}
	})
	h.dirty = true
	h.mu.Unlock()
	lt.read(len(data), feed)
}

func (h *Holder) waitLoop() {
	err := h.cmd.Wait()
	newKills := h.reap()
	// The agent's last output may still be in the PTY. Let the reader take
	// it first, so that clients see it before they hear of the end. A
	// process the agent left behind can keep the PTY open: wait a moment only.
	select {
	case <-h.readDone:
	case <-time.After(time.Second):
	}
	ended, killed := endOf(err)
	// New kills and a SIGKILL: the cap stopped the agent. New kills without
	// one: it stopped other processes, which no tick has claimed yet.
	ended.OOM = killed && newKills
	childOOM := newKills && !killed
	h.log.Info("agent exited", "code", ended.ExitCode, "signal", ended.Signal, "oom", ended.OOM, "child_oom", childOOM)
	h.mu.Lock()
	h.exited = true
	h.ended = ended
	if h.stopping {
		// The holder stopped the agent because the system is stopping: keep
		// the session's state, so it is restored after the restart.
		h.mu.Unlock()
		return
	}
	code := ended.ExitCode
	st := Status{State: StateExited, Since: time.Now().UTC(), ExitCode: &code, Signal: ended.Signal, OOM: ended.OOM, Source: "process"}
	if ended.OOM {
		st.State = StateErrored
		st.Reason = "oom"
	} else if code != 0 {
		st.State = StateErrored
	}
	if childOOM {
		// Not the OOM flag: on an ended session, it means the cap stopped
		// the agent itself.
		st.Reason = oomChildReason
		h.alertOOM()
	}
	h.m.Status = st
	h.alertEnded(ended)
	b, _ := json.Marshal(ended)
	for c := range h.clients {
		if c.attached {
			select {
			case c.out <- outFrame{proto.TEnded, b}:
			default:
			}
		}
	}
	// Save a copy: other goroutines change the manifest under the lock.
	m := *h.m
	h.mu.Unlock()
	_ = h.save(&m, false)
}

// tick updates the status and saves the manifest when it changed.
func (h *Holder) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		h.step()
	}
}

// step is one tick: it reads the unit's memory files, updates the status,
// and saves the manifest when it changed.
func (h *Holder) step() {
	mem := h.readMem()
	lt := h.lockTimed()
	h.checkMemory(mem)
	// Before updateStatus: a kill that it claims clears the memory warning's
	// mark, and the status shows that in the same tick.
	h.checkOOM(mem.kills)
	if !h.exited {
		h.updateStatus()
	}
	dirty := h.dirty
	h.dirty = false
	var snapshot Manifest
	if dirty {
		snapshot = *h.m
	}
	h.mu.Unlock()
	lt.tick()
	if dirty {
		if err := h.save(&snapshot, false); err != nil {
			h.log.Warn("manifest not saved", "err", err)
		}
	}
}

// updateStatus sets the session's state from the screen rules, the agent's
// hooks and notifications, the memory warning, and its output. The caller
// holds h.mu.
func (h *Holder) updateStatus() {
	if h.scr != nil && h.scr.dirty {
		h.scr.dirty = false
		h.ruleState, h.ruleID, _ = agents.Detect(h.m.Agent.Kind, h.scr.screen())
		if full := h.scr.altScreen(); full != h.m.Status.FullScreen {
			h.m.Status.FullScreen = full
			h.dirty = true
		}
	}
	state, src := h.agentState()
	agentPrev := h.al.agent
	h.al.agent = state
	if h.mem.mark && state != StateNeedsInput {
		// Close to the memory cap (holder_mem.go), after the agent's own wait.
		state, src = StateNeedsInput, srcMemory
	}
	// The wait can also pass between the agent and the memory warning.
	handover := state == StateNeedsInput && (src == srcMemory) != (h.m.Status.Source == srcMemory)
	if state != h.m.Status.State || handover {
		oom := h.m.Status.OOM
		h.m.Status = Status{State: state, Since: time.Now().UTC(), Source: src, FullScreen: h.m.Status.FullScreen, OOM: oom}
		switch {
		case src == srcMemory:
			h.m.Status.Reason = h.mem.reason()
			h.mem.shown = time.Now()
		case state == StateNeedsInput:
			// The agent's question, if its hook gave one.
			h.m.Status.Reason = h.notifyMsg
		case oom:
			// The memory cap stopped a process that the agent started.
			h.m.Status.Reason = oomChildReason
		}
		h.dirty = true
	}
	if h.al.agent != agentPrev {
		// Alerts follow the agent's own state, so its turns and questions
		// count while the memory warning shows too.
		h.alertState(agentPrev, h.al.agent)
	}
}

// agentState is the session's state from the agent's own signals, without
// the memory warning. The caller holds h.mu.
func (h *Holder) agentState() (state, src string) {
	state, src = StateIdle, "quiet"
	switch {
	case h.ruleState == StateNeedsInput:
		state, src = StateNeedsInput, "screen:"+h.ruleID
	case h.needInput:
		state, src = StateNeedsInput, "notify"
	case h.ruleState == StateWorking:
		state, src = StateWorking, "screen:"+h.ruleID
	case h.hookBusy:
		state, src = StateWorking, "hook"
	case h.busy:
		state, src = StateWorking, "progress"
	case h.hookSeen && !h.lastTurnEnd.IsZero() && time.Since(h.lastTurnEnd) < 3*time.Second:
		state, src = StateIdle, "hook"
	case time.Since(h.lastOut) < 3*time.Second:
		state, src = StateWorking, "output"
	}
	return state, src
}
