package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
)

// sandbox is one session's sandbox, as its driver builds it: the part of
// spec 5.1's wall.Sandbox that this version uses.
type sandbox interface {
	// Start boots the sandbox and returns the link to its guest init.
	Start(ctx context.Context) (net.Conn, error)
	// Kill ends every process of the sandbox that the driver can reach.
	// It is the sandbox's one cleanup: it may run more than once, at any
	// moment.
	Kill() error
	// Wait returns when the sandbox has ended.
	Wait() error
}

// driverPlain is the test driver of spec 5.1.
const driverPlain = "plain"

// sandboxFor is newSandbox. Tests replace it with a fake guest.
var sandboxFor = newSandbox

// newSandbox returns the sandbox of session m, from the driver that its
// trusted manifest names.
func newSandbox(m *host.Manifest) (sandbox, error) {
	if m.Wall.Driver == "process" {
		return prepareProcess(m)
	}
	if m.Wall.Driver == driverPlain {
		return &plain{id: m.ID, root: paths.SandboxRoot(m.ID), exited: make(chan struct{})}, nil
	}
	return nil, &proto.Error{Code: "wall_setup", Message: "this helper has no wall driver " + m.Wall.Driver,
		Hint: "Update the host helper, then resume the session."}
}

// plain is the test driver. It is not a wall: it starts the guest init as
// a child, with no namespaces, and builds nothing around it. The guest's
// own folders, /run/<name> and /home/agent, live in paths.SandboxRoot,
// which stands for the sandbox's root. The guest init gets the link as one
// end of a socket pair, as with the process wall, so nothing listens.
//
// Each process signals only its own children that it has not reaped yet,
// and the groups that they lead: their IDs cannot name another process
// until then. The driver signals the guest init, its child, and the guest
// init's group. The guest init signals the holder, and its other children
// and their groups at its end. The holder signals the agent's group, and
// keeps the agent unreaped until its stop has sent the group its last
// SIGKILL. On Linux, the guest init is a child subreaper, so a process of
// the sandbox whose parent dies becomes its child, and its end reaps them
// all. Each process of that chain also gets a signal when its parent dies
// (host.StopWithParent, host.DieWithParent).
//
// So a process escapes only when it is in neither the guest init's group
// nor the agent's, after setsid or setpgid, and then loses the process
// that would end it. On Linux, that happens only when the guest init dies
// at once, by SIGKILL, before its end. macOS has no subreaper and no
// parent-death signal, so there it also happens when the process's own
// parent dies before the sandbox ends. The process wall of a later
// version contains the sandbox in the kernel
// (docs/explanation/architecture.md, 2.5).
type plain struct {
	id   string
	root string

	mu     sync.Mutex
	cmd    *exec.Cmd
	closed bool          // Kill has run to its end
	exited chan struct{} // closed when the guest init has exited: it stays unreaped until Kill
	// clean is true when the guest init exited with code 0, which it does
	// only after its end ran: it stopped its holder and its other children.
	clean  atomic.Bool
	killMu sync.Mutex
}

// The waits of Kill.
var (
	// plainGrace is how long the guest init gets to end its sandbox after
	// SIGTERM: its holder's grace and wait (guest.holderWait), and more.
	plainGrace = 10 * time.Second
	// groupGrace is how long the guest init's group gets after the guest
	// init exited without its end, by a signal or a crash: the holder's
	// own wait for its agent, and a second.
	groupGrace = 4 * time.Second
)

// signalGroup sends sig to the process group that pid leads. Tests watch
// it.
var signalGroup = func(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }

// initArgs are the arguments of the helper as the guest init. Tests add
// one, for a hostile guest init.
var initArgs = []string{"wall-init"}

// reapWait bounds the reap of a guest init that SIGKILL ended.
const reapWait = 5 * time.Second

// plainEnv is the guest init's environment: a fixed base, never the
// supervisor's own. The race detector's and the coverage's variables of a
// test pass on.
func plainEnv(root string) []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", guest.RootEnv + "=" + root}
	for _, k := range []string{"GORACE", "GOCOVERDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Start makes the root folder and the socket pair, and starts the guest
// init with its end on fd 3. The guest init's output goes to the session's
// guest-init.log, outside the root. Each start makes the root anew, once
// the holder of an earlier run of the session has gone (waitEnded). A stop
// (the end of ctx) before the guest init starts ends the start: no guest
// starts after it.
func (p *plain) Start(ctx context.Context) (net.Conn, error) {
	if err := waitEnded(ctx, guest.Files(p.root).Sock); err != nil {
		return nil, err
	}
	if err := waitEnded(ctx, guest.FilesFor(p.root, p.id).Sock); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, wallError("the start ended before the sandbox's root was made", err)
	}
	keepHolderLog(p.root, p.id) // a run that a crash ended left it here
	_ = os.RemoveAll(p.root)    // a start begins fresh, as a tmpfs would
	if err := paths.EnsurePrivateDir(p.root); err != nil {
		return nil, wallError("cannot make the sandbox's root folder", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, wallError("cannot find the helper", err)
	}
	logf, err := openInitLog(paths.GuestInitLog(p.id))
	if err != nil {
		return nil, wallError("cannot open the guest init's log", err)
	}
	defer logf.Close()
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		return nil, wallError("cannot make the link", err)
	}
	defer theirs.Close()
	// The guest init outlives ctx, which bounds only the start.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, initArgs...)
	cmd.Env = plainEnv(p.root)
	cmd.ExtraFiles = []*os.File{theirs}
	// A file, not a pipe: the guest init and the holder write their own
	// output, also after the supervisor died, when no one would read a
	// pipe.
	cmd.Stdout, cmd.Stderr = logf, logf
	// A process that keeps the output open does not hold up the reap.
	cmd.WaitDelay = time.Second
	// A session of its own, which it leads: it cannot leave its group, and
	// no process of the sandbox can join a group of the supervisor's
	// session. It ends its sandbox when the supervisor dies.
	cmd.SysProcAttr = host.StopWithParent(&syscall.SysProcAttr{Setsid: true})
	beforeInit()
	if err := ctx.Err(); err != nil {
		ours.Close()
		return nil, wallError("the start ended before the guest init started", err)
	}
	if err := cmd.Start(); err != nil {
		ours.Close()
		return nil, wallError("cannot start the guest init", err)
	}
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	go func() {
		ws, ok := proc.AwaitStatus(cmd.Process.Pid)
		p.clean.Store(ok && ws.Exited() && ws.ExitStatus() == 0)
		close(p.exited)
	}()
	return ours, nil
}

// beforeInit runs just before the guest init starts. Tests end the start
// there.
var beforeInit = func() {}

// oldWait bounds the wait for an earlier sandbox of the session.
var oldWait = 10 * time.Second

// waitEnded waits, up to oldWait, until the holder's socket sock has gone,
// and fails when it cannot confirm that. A sandbox of an earlier run of
// the session may still be ending, after its supervisor died: its holder
// gets SIGTERM when its guest init dies, stops its agent first, saves its
// manifest, and removes its socket last. The new sandbox's root, which is
// the same folder, is then its own. A socket that nothing listens on any
// more is gone too. The end of ctx ends the wait, with an error: a stop
// starts no guest.
func waitEnded(ctx context.Context, sock string) error {
	d := net.Dialer{Timeout: 100 * time.Millisecond}
	for deadline := time.Now().Add(oldWait); ; {
		if _, err := os.Lstat(sock); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		c, err := d.DialContext(ctx, "unix", sock)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil
		}
		if err == nil {
			c.Close()
		}
		if err := ctx.Err(); err != nil {
			return wallError("the start ended while an earlier run of the session ended", err)
		}
		if !time.Now().Before(deadline) {
			return &proto.Error{Code: "wall_setup", Message: "the holder of an earlier run of the session still answers after " + oldWait.String(),
				Hint: "Wait a minute for it to end, then resume the session. If it stays, run the doctor."}
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Kill ends the sandbox, then reaps the guest init, keeps the holder's log
// and closes the guest init's log. It sends SIGTERM to the guest init's
// group, so that each process there ends what it started: the guest init
// stops its holder and reaps its other children, and the holder ends its
// agent's group. It waits for the guest init's exit, or plainGrace. When
// the guest init exited without its end, by a signal or a crash, it gives
// the group groupGrace more. Then it sends SIGKILL to the group and to
// the guest init. The guest init stays unreaped until then, so its PID and
// the group's ID are still the sandbox's. Every wait of Kill is bounded.
func (p *plain) Kill() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	p.mu.Lock()
	cmd, closed := p.cmd, p.closed
	p.mu.Unlock()
	if cmd == nil || closed {
		return nil
	}
	pid := cmd.Process.Pid
	_ = signalGroup(pid, syscall.SIGTERM)
	select {
	case <-p.exited:
		if !p.clean.Load() {
			time.Sleep(groupGrace)
		}
	case <-time.After(plainGrace):
	}
	_ = signalGroup(pid, syscall.SIGKILL)
	// The guest init itself too, by its PID, which is pinned while it is
	// unreaped.
	_ = cmd.Process.Kill()
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	select {
	case <-reaped:
	case <-time.After(reapWait):
	}
	keepHolderLog(p.root, p.id)
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// Wait returns when the guest init has exited.
func (p *plain) Wait() error {
	p.mu.Lock()
	started := p.cmd != nil
	p.mu.Unlock()
	if started {
		<-p.exited
	}
	return nil
}
