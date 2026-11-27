package guest

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// maxEvent is the largest event that the guest init passes on: the cap of
// spec 10.3, which the supervisor checks again.
const maxEvent = 64 << 10

// startWait is how long the guest init waits for the holder to start the
// agent.
const startWait = 10 * time.Second

// Files are the holder's files in a sandbox whose root folder is root: ""
// in a real wall. They are in /run/<name>, with the hook socket that the
// agent's environment names (spec 7.1).
func Files(root string) host.GuestHolder {
	g := &Init{root: root}
	run := g.path(host.GuestRunDir())
	return host.GuestHolder{Sock: filepath.Join(run, "holder.sock"), Hook: g.path(host.GuestHookSocket()), Manifest: filepath.Join(run, "holder.json")}
}

// holderProc is the session's holder, a child of the guest init.
type holderProc struct {
	runToken   string
	agentEnded bool // Init.mu protects the lifetime of this session, not the holder UI
	id         string
	files      host.GuestHolder
	cmd        *exec.Cmd
	done       chan struct{} // closed when the holder has exited and is reaped
	// eventsDone closes before passEvents can ask the init to end, so
	// shutdown may join forwarding without recursing through g.once.
	eventsDone chan struct{}
	events     net.Conn
	eventsOnce sync.Once
}

// holderWait is how long the holder gives its agent between SIGHUP and
// SIGKILL when it stops (holder_proc.go). The guest init waits longer.
const holderWait = 3 * time.Second

// start writes the holder's manifest, starts the holder in guest mode,
// hands it the session's secrets and waits until it has started the
// agent. It returns the agent's process ID.
func (g *Init) start(a proto.CtlStartArgs) (int, error) { return g.startMember(a, true) }

func (g *Init) startMember(a proto.CtlStartArgs, restore bool) (int, error) {
	if !api.ValidSessionID(a.Session) {
		return 0, &proto.Error{Code: "bad_request", Message: "start names no valid session",
			Hint: "Resume the session, so that its supervisor starts it again."}
	}
	if _, err := memberBoundSentinels(a); err != nil {
		return 0, err
	}
	if g.vm != nil {
		if err := g.vm.validateMember(a, restore); err != nil {
			return 0, err
		}
	}
	if a.PreparedClaim != "" {
		var err error
		a, err = g.consumePreparedMember(a, restore)
		if err != nil {
			return 0, err
		}
	} else {
		if a.SecretModes != nil || a.InputNonce != "" || a.InputDigest != "" {
			return 0, execError("this member requires completed preparation")
		}
		if err := g.projectMember(&a, restore); err != nil {
			return 0, err
		}
	}
	files := g.memberFilesV1(a.Session)
	// The explicitly installed no-wall fixture retains its legacy primary
	// layout, so P14 lifecycle assertions stay unchanged. Production never
	// enables this from RootEnv or another input.
	if g.vm == nil && restore && g.root != "" && plainHolderSpawnerForTests != nil {
		files = Files(g.root)
	}
	run := filepath.Dir(files.Sock)
	if err := paths.EnsurePrivateDir(run); err != nil {
		return 0, err
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(files.Manifest)); err != nil {
		return 0, err
	}
	home := g.path(host.GuestHome)
	if g.unwalled == nil {
		if err := paths.EnsurePrivateDir(home); err != nil {
			return 0, err
		}
	}
	if err := writeManifest(g, a, files.Manifest); err != nil {
		return 0, err
	}
	hp, err := g.startHolder(a.Session, files, home, a.RunToken)
	if err != nil {
		return 0, err
	}
	if err := hp.answers(5 * time.Second); err != nil {
		if g.vm != nil {
			hp.stop(0)
		}
		return 0, err
	}
	if g.vm != nil {
		pid, err := g.vm.startAgent(a, hp)
		if err != nil {
			hp.stop(0)
		}
		if err == nil && restore {
			g.mu.Lock()
			g.execEnv, g.execCwd = g.vm.childEnvironment(a.Env), a.Cwd
			g.mu.Unlock()
		}
		return pid, err
	}
	if len(a.Secrets) > 0 {
		env := make(map[string]api.Secret, len(a.Secrets))
		for k, v := range a.Secrets {
			env[k] = api.Secret(v)
		}
		if _, err := host.HolderRequestAt(files.Sock, "secrets", map[string]any{"env": env}, 5*time.Second); err != nil {
			return 0, &proto.Error{Code: "secrets_failed", Message: "the holder did not take the secrets: " + err.Error(), Hint: "Resume the session."}
		}
	}
	pid, err := hp.started(startWait)
	if err == nil && restore {
		g.mu.Lock()
		g.execEnv = append([]string(nil), a.Env...)
		g.execCwd = a.Cwd
		g.mu.Unlock()
	}
	return pid, err
}

// writeManifest writes the holder's manifest inside the sandbox: what the
// holder needs to start the agent. The trusted manifest stays outside.
func writeManifest(g *Init, a proto.CtlStartArgs, path string) error {
	now := time.Now().UTC()
	names := make([]string, 0, len(a.Secrets))
	for k := range a.Secrets {
		names = append(names, k)
	}
	env := g.mapEnv(a.Env)
	m := &host.Manifest{
		Schema: 1, ID: a.Session, Name: a.Session, CreatedAt: now,
		Agent:   host.AgentInfo{Kind: a.Agent, Argv: a.Argv, Hooks: a.Hooks},
		Cwd:     g.mapValue(a.Cwd),
		Env:     env,
		Term:    host.TermInfo{Term: envValue(env, "TERM"), Cols: a.Cols, Rows: a.Rows},
		Status:  host.Status{State: host.StateStarting, Since: now},
		Secrets: names,
		// The holder says in the agent's terminal that these do not come.
		MissingSecrets: a.Missing,
	}
	if a.Restored {
		host.MarkRestored(m)
	}
	if err := m.SaveTo(path); err != nil {
		return &proto.Error{Code: "wall_init", Message: "the guest init cannot write the holder's manifest: " + err.Error(),
			Hint: "Check the sandbox's free space, then resume the session."}
	}
	return nil
}

// startHolder starts the helper as the session's holder, in guest mode,
// with one end of a socket pair for its events on fd 3. Its environment is
// the guest init's own, with the guest's home: a holder in guest mode
// keeps its own files there. It gets SIGTERM when the guest init dies, so
// that it can end its agent's group first (host.StopWithParent). No holder
// starts once the guest init ends.
func (g *Init) startHolder(id string, files host.GuestHolder, home string, runToken string) (*holderProc, error) {
	exe, err := g.helperExecutable()
	if err != nil {
		return nil, &proto.Error{Code: "wall_init", Message: "the guest init cannot find the helper: " + err.Error(), Hint: "Resume the session."}
	}
	ours, theirs, err := SocketPair()
	if err != nil {
		return nil, err
	}
	defer theirs.Close()
	if g.lifetime == nil {
		_ = ours.Close()
		return nil, execError("the holder has no guest lifetime")
	}
	// Init's existing stop owner must deliver graceful stop before hard kill.
	// Retain its context values without a competing CommandContext killer.
	cmd := exec.CommandContext(context.WithoutCancel(g.lifetime), exe, "holder", id)
	cmd.Env = append(holderEnv(os.Environ(), home), files.Env()...)
	if g.unwalled != nil {
		cmd.Env = append(append([]string(nil), os.Environ()...), files.Env()...)
	}
	cmd.ExtraFiles = []*os.File{theirs}
	if g.unwalled != nil && g.unwalled.helper != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, g.unwalled.helper)
	}
	if g.vm != nil {
		release, err := g.vm.configureHolder(cmd, id, runToken, files)
		if err != nil {
			ours.Close()
			return nil, err
		}
		defer release()
	}
	// The holder writes its own log. A crash still shows in the guest
	// init's.
	cmd.Stderr = os.Stderr
	attr, err := g.holderProcess()
	if err != nil {
		ours.Close()
		return nil, err
	}
	// Process/gVisor guest ABI v1: holder and its PTY agent both run as1000.
	// VM holder900 requires guest-owned-agent-pty-v2, never root inheritance.
	attr.Setpgid = false
	cmd.SysProcAttr = host.StopWithParent(attr)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended || g.lifetime.Err() != nil {
		ours.Close()
		return nil, &proto.Error{Code: "stopped", Message: "the sandbox stops, so it starts no holder", Hint: "Resume the session."}
	}
	start := cmd.Start
	if g.vm != nil {
		start = func() error { return g.vm.startHolderCommand(cmd) }
	}
	if err := start(); err != nil {
		ours.Close()
		return nil, &proto.Error{Code: "wall_init", Message: "the guest init cannot start the holder: " + err.Error(), Hint: "Resume the session."}
	}
	hp := &holderProc{runToken: runToken, id: id, files: files, cmd: cmd, done: make(chan struct{}),
		eventsDone: make(chan struct{}), events: ours}
	if g.members == nil {
		g.members = make(map[string]*holderProc)
	}
	g.members[id] = hp
	if id == g.primary || g.holder == nil {
		g.holder = hp
	}
	go func() {
		_ = cmd.Wait()
		close(hp.done)
	}()
	go g.passEvents(ours, hp)
	return hp, nil
}

// holderEnv is the holder's own environment: env without the host's home
// and folders, and with the guest's home.
func holderEnv(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == DNSFDSEnv || k == "HOME" || strings.HasPrefix(k, "XDG_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// passEvents reads the holder's events and passes each to the supervisor.
// When the holder goes, the guest init ends too: the supervisor then sees
// the sandbox end.
func (g *Init) passEvents(conn net.Conn, hp *holderProc) {
	defer close(hp.eventsDone)
	defer hp.closeEvents()
	r := bufio.NewReader(conn)
	var head [4]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			break
		}
		n := binary.LittleEndian.Uint32(head[:])
		if n == 0 || n > maxEvent {
			break
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			break
		}
		if proto.Type(buf[0]) == proto.TEvent {
			g.holderEvent(hp, buf[1:])
		}
	}
	<-hp.done
	g.log.Info("the holder exited")
	g.endIfEmpty()
}

// closeEvents releases a holder event read once, also when a descendant
// keeps the inherited writer open after the holder's process exits.
func (hp *holderProc) closeEvents() {
	if hp != nil {
		hp.eventsOnce.Do(func() { _ = hp.events.Close() })
	}
}

// exited reports whether the holder has exited.
func (hp *holderProc) exited() bool {
	if hp == nil {
		return false
	}
	select {
	case <-hp.done:
		return true
	default:
		return false
	}
}

// answers waits until the holder's socket answers, or the holder exits.
func (hp *holderProc) answers(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		if c, err := d.DialContext(context.Background(), "unix", hp.files.Sock); err == nil {
			c.Close()
			return nil
		}
		select {
		case <-hp.done:
			return hp.failed()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return &proto.Error{Code: "start_failed", Message: "the holder did not answer in time", Hint: "Resume the session."}
}

// started waits until the holder's manifest records the agent's start, or
// its failure, and returns the agent's process ID.
func (hp *holderProc) started(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		m, err := host.LoadManifestFile(hp.files.Manifest)
		switch {
		case err == nil && m.Run.AgentPID != 0:
			return m.Run.AgentPID, nil
		case err == nil && m.Status.State == host.StateErrored:
			return 0, &proto.Error{Code: "start_failed", Message: m.Status.Reason, Hint: "Check the agent's command, then resume the session."}
		case time.Now().After(deadline):
			return 0, &proto.Error{Code: "start_failed", Message: "the agent did not start in time", Hint: "Resume the session."}
		}
		select {
		case <-hp.done:
			return 0, hp.failed()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// failed is the error of a holder that exited before the agent started:
// the reason in its manifest, when it wrote one.
func (hp *holderProc) failed() error {
	if m, err := host.LoadManifestFile(hp.files.Manifest); err == nil && m.Status.Reason != "" {
		return &proto.Error{Code: "start_failed", Message: m.Status.Reason, Hint: "Check the agent's command, then resume the session."}
	}
	return &proto.Error{Code: "start_failed", Message: "the holder exited before the agent started", Hint: "Resume the session."}
}

// stop asks the holder to stop, and waits up to grace. It then kills the
// holder. The holder ends its agent and the agent's group itself, with
// its own wait, so the guest init waits for the grace and that wait, and a
// second more. The guest init signals only the holder, its own child,
// through os.Process, which never signals a PID that it has reaped. On
// Linux, an agent or a process of its that outlives the holder becomes the
// guest init's child, and its end takes it (reapOrphans).
func (hp *holderProc) stop(grace time.Duration) {
	if hp == nil {
		return
	}
	_ = hp.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-hp.done:
	case <-time.After(grace + holderWait + time.Second):
		_ = hp.cmd.Process.Kill()
		<-hp.done
	}
}

// serveHolder carries one holder stream to the holder's socket, both ways,
// byte for byte. The stream's header names the session, and a stream for
// another session is refused (spec 6.3).
func (g *Init) serveHolder(st *link.Stream) {
	var hdr struct {
		Session string `json:"session"`
	}
	g.mu.Lock()
	if st.Header(&hdr) != nil {
		g.mu.Unlock()
		st.Close()
		return
	}
	hp := g.members[hdr.Session]
	g.mu.Unlock()
	if hp == nil {
		_ = st.Close()
		return
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(context.Background(), "unix", hp.files.Sock)
	if err != nil {
		_ = st.Close()
		return
	}
	if g.vm != nil {
		release, err := g.vm.holderStream(st, conn)
		if err != nil {
			conn.Close()
			st.Close()
			return
		}
		defer release()
	}
	pipe(st, conn)
}

// pipe copies a and b into each other until both directions end, then
// closes both. Each direction passes its end on as a half-close.
func pipe(a *link.Stream, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		if u, ok := b.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		_ = a.CloseWrite()
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}

// envValue is the value of key in env, or "".
func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func (g *Init) holderEvent(hp *holderProc, p []byte) {
	var ev proto.CtlEvent
	if json.Unmarshal(p, &ev) != nil || ev.Session != hp.id || ev.Event == proto.EventHeartbeat {
		return
	}
	if ev.Event == proto.EventEnded {
		g.mu.Lock()
		hp.agentEnded = true
		g.mu.Unlock()
	}
	ev.RunToken = hp.runToken
	p, err := json.Marshal(ev)
	if err != nil {
		return
	}
	g.event(p)
	if ev.Event == proto.EventEnded {
		g.endIfEmpty()
	}
}
