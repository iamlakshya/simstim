package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proc/proctest"
	"github.com/iamlakshya/simstim/internal/version"
)

// The tests of this binary start it again as a guest init and a holder,
// under the test driver.
func TestMain(m *testing.M) {
	guest.SetPlainHolderSpawnerForTest(func() (*syscall.SysProcAttr, error) { return &syscall.SysProcAttr{}, nil })
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "wall-init":
			hostileInit(os.Args[2:])
			if err := guest.Run(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "holder":
			if err := host.RunHolder(os.Args[2]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// The hostile guest inits that a test asks for with initArgs.
const (
	// initStays tries to join its parent's group, writes the error of
	// that try to the file that follows, and stays alive after its link's
	// end and its own end, until SIGKILL.
	initStays = "stays"
	// initChild leaves a process in its own group first, which ignores
	// SIGHUP and SIGTERM, and writes the process's PID to the file that
	// follows. The process is no child of the guest init: its parent exits
	// at once, before the guest init is a subreaper. So only the driver's
	// SIGKILL to the group ends it.
	initChild = "child"
	// initCrash runs the guest init, and exits with code 2 after the
	// seconds that follow, as a crash would: its end never runs.
	initCrash = "crash"
	// initTermChild starts a child in its own group first, which writes
	// the file that follows when SIGTERM comes, then exits.
	initTermChild = "term-child"
)

// hostileInit plays the hostile guest init that args name, if any.
func hostileInit(args []string) {
	switch {
	case len(args) >= 2 && args[0] == initStays:
		err := syscall.Setpgid(0, mustPgid(os.Getppid()))
		_ = os.WriteFile(args[1], []byte(fmt.Sprint(err)), 0o600)
		_ = guest.Run()
		select {}
	case len(args) >= 2 && args[0] == initChild:
		c := exec.CommandContext(context.Background(), "/bin/sh", "-c",
			"(trap '' HUP TERM; echo $(exec sh -c 'echo $PPID') > '"+args[1]+"'; exec sleep 300) &")
		_ = c.Run()
	case len(args) >= 2 && args[0] == initCrash:
		d, _ := strconv.ParseFloat(args[1], 64)
		time.AfterFunc(time.Duration(d*float64(time.Second)), func() { os.Exit(2) })
	case len(args) >= 2 && args[0] == initTermChild:
		c := exec.CommandContext(context.Background(), "/bin/sh", "-c",
			"trap '' HUP; trap 'echo TERM > \""+args[1]+"\"; exit 0' TERM; echo ready > '"+args[1]+".ready'; while :; do sleep 0.1; done")
		_ = c.Start()
	}
}

// mustPgid is the process group of pid, or 0.
func mustPgid(pid int) int {
	g, _ := syscall.Getpgid(pid)
	return g
}

// hostileDriver makes the driver start the hostile guest init of args.
func hostileDriver(t *testing.T, args ...string) {
	t.Helper()
	old, oldGrace := initArgs, plainGrace
	initArgs = append([]string{"wall-init"}, args...)
	plainGrace = time.Second
	t.Cleanup(func() { initArgs, plainGrace = old, oldGrace })
}

// plainSession saves the manifest of a session under the test driver,
// whose agent and a child of it that stays in its group ignore SIGHUP and
// SIGTERM, and write their process IDs. It returns the session's ID and a
// function that waits for both IDs.
func plainSession(t *testing.T) (id string, pids func() (agent, child int)) {
	t.Helper()
	dir := testFolders(t)
	pidf, childf := filepath.Join(dir, "agent.pid"), filepath.Join(dir, "child.pid")
	id = "plain234"
	t0 := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: t0, Cwd: dir,
		Agent: host.AgentInfo{Kind: "custom", Argv: []string{"/bin/sh", "-c",
			"trap '' HUP TERM; echo $$ > '" + pidf + "'; sleep 300 & echo $! > '" + childf + "'; exec sleep 300"}},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=" + host.GuestHome, brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(), "TERM=xterm-256color"},
		Term: host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24}, Status: host.Status{State: host.StateStarting, Since: t0},
		Wall: &host.WallRecord{Level: "none", Driver: driverPlain, Version: version.Version, At: t0}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	read := func(f string) int {
		var pid int
		waitFor(t, "the pid in "+filepath.Base(f), func() bool {
			b, err := os.ReadFile(f)
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			return err == nil && pid > 0
		})
		pinned(t, pid, "sleep")
		return pid
	}
	return id, func() (int, int) { return read(pidf), read(childf) }
}

// pinned holds pid, a process that runs word, until the test's end, and
// then sends it SIGKILL, in case the code under test left it: through a
// pidfd on Linux, which never reaches another process. Off Linux, a test
// holds only its own children, so a process that the test did not start
// is not held, and ends by itself.
func pinned(t *testing.T, pid int, word string) {
	t.Helper()
	if h := proctest.Pin(pid, word); h != nil {
		t.Cleanup(func() {
			_ = h.Signal(syscall.SIGKILL)
			h.Close()
		})
	}
}

// goneWithin waits up to d until each process of pids has gone, or waits
// as a zombie. It reaps nothing.
func goneWithin(t *testing.T, d time.Duration, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(d)
	for _, pid := range pids {
		for !processGone(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d outlived its sandbox by %s", pid, d)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// reaped fails the test unless the guest init pid, a child of the test's
// process, is reaped: the driver's cleanup reaps it. The check reaps
// nothing.
func reaped(t *testing.T, pid int) {
	t.Helper()
	if !proctest.Reaped(pid) {
		t.Errorf("the guest init %d is not reaped", pid)
	}
}

// The test driver gives the guest init a fixed base, never the
// supervisor's own environment: the process that it starts has no other.
func TestPlainGivesAFixedEnvironment(t *testing.T) {
	testFolders(t)
	t.Setenv("SOME_LOGIN_SECRET", "s3cret")
	if err := paths.EnsurePrivateDir(paths.SessionDir("envs2345")); err != nil {
		t.Fatal(err)
	}
	sb, err := newSandbox(&host.Manifest{ID: "envs2345", Wall: &host.WallRecord{Driver: driverPlain}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sb.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		_ = sb.Kill()
	})
	env := processEnv(t, sb.(*plain).cmd.Process.Pid)
	if strings.Contains(env, "SOME_LOGIN_SECRET") || strings.Contains("\n"+env, "\nHOME=") {
		t.Errorf("the guest init's environment holds the supervisor's:\n%s", env)
	}
	for _, want := range []string{"PATH=/usr/local/bin:/usr/bin:/bin", guest.RootEnv + "=" + paths.SandboxRoot("envs2345")} {
		if !strings.Contains(env, want) {
			t.Errorf("the guest init's environment lacks %s:\n%s", want, env)
		}
	}
}

// The test driver attaches a regular log file directly to the guest init's
// output and error output, never a pipe whose reader goes with the
// supervisor.
func TestPlainAttachesARegularLogFile(t *testing.T) {
	testFolders(t)
	const id = "logf2345"
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	sb, err := newSandbox(&host.Manifest{ID: id, Wall: &host.WallRecord{Driver: driverPlain}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sb.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		_ = sb.Kill()
	})
	p := sb.(*plain)
	outf, okOut := p.cmd.Stdout.(*os.File)
	errf, okErr := p.cmd.Stderr.(*os.File)
	if !okOut || !okErr {
		t.Fatalf("guest init output = (%T, %T), want *os.File on both so os/exec makes no pipe", p.cmd.Stdout, p.cmd.Stderr)
	}
	want := paths.GuestInitLog(id)
	if outf.Name() != want || errf.Name() != want {
		t.Fatalf("guest init log files = (%q, %q), want %q", outf.Name(), errf.Name(), want)
	}
	fi, err := os.Lstat(want)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("guest init log %s mode = %v, err = %v, want regular file", want, fi, err)
	}
}

// watched is a test driver whose guest init the test can see.
func watched(t *testing.T) <-chan *plain {
	t.Helper()
	got := make(chan *plain, 4)
	old := sandboxFor
	sandboxFor = func(m *host.Manifest) (sandbox, error) {
		sb, err := newSandbox(m)
		if p, ok := sb.(*plain); ok {
			got <- p
		}
		return sb, err
	}
	t.Cleanup(func() { sandboxFor = old })
	return got
}

// startPlain runs the supervisor of session id under the test driver, and
// returns it, its driver and its end, once the session has started.
func startPlain(t *testing.T, id string) (*Supervisor, *plain, chan error) {
	t.Helper()
	drivers := watched(t)
	sup := make(chan *Supervisor, 1)
	testStarted = func(s *Supervisor) { sup <- s }
	t.Cleanup(func() { testStarted = nil })
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	select {
	case s := <-sup:
		return s, <-drivers, ended
	case err := <-ended:
		t.Fatalf("the session did not start: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the session did not start")
	}
	return nil, nil, nil
}

// A breach of the link ends the sandbox under the test driver, with every
// process in it: an agent that ignores SIGHUP and SIGTERM, and a child of
// it in its group. The session ends as an error, and raises sandbox_error.
func TestABreachEndsTheWholeSandbox(t *testing.T) {
	id, pids := plainSession(t)
	got := alertsTo(t)
	s, p, ended := startPlain(t, id)
	agent, child := pids()
	s.breach(errors.New("a test's breach"))
	select {
	case <-ended:
	case <-time.After(20 * time.Second):
		t.Fatal("the supervisor did not end after the breach")
	}
	goneWithin(t, 10*time.Second, agent, child)
	reaped(t, p.cmd.Process.Pid)
	m, err := host.LoadManifest(id)
	if err != nil || m.Status.State != host.StateErrored || m.Status.Reason != "the sandbox broke the link's rules" {
		t.Errorf("after the breach: %+v, %v", m.Status, err)
	}
	waitAlerts(t, got, alerts.SandboxError)
}

// The driver signals only its own child, the guest init, and the group
// that it leads, and only while the guest init is unreaped. A PID that a
// guest's file names, here a decoy's outside the sandbox, is never
// signalled.
func TestPlainSignalsOnlyItsUnreapedChild(t *testing.T) {
	decoy := exec.CommandContext(context.Background(), "sleep", "60")
	decoy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := decoy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = decoy.Process.Kill(); _ = decoy.Wait() })
	id, pids := plainSession(t)
	var mu sync.Mutex
	var bad []string
	var p *plain
	old := signalGroup
	signalGroup = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		if p == nil || pid != p.cmd.Process.Pid || p.cmd.ProcessState != nil {
			bad = append(bad, fmt.Sprintf("%v to group %d", sig, pid))
		}
		mu.Unlock()
		return old(pid, sig)
	}
	t.Cleanup(func() { signalGroup = old })
	s, drv, ended := startPlain(t, id)
	mu.Lock()
	p = drv
	mu.Unlock()
	_, _ = pids()
	f := guest.Files(paths.SandboxRoot(id)).Manifest
	m, err := host.LoadManifestFile(f)
	if err != nil {
		t.Fatal(err)
	}
	m.Run.AgentPID = decoy.Process.Pid
	if err := m.SaveTo(f); err != nil {
		t.Fatal(err)
	}
	s.breach(errors.New("a test's breach"))
	<-ended
	mu.Lock()
	defer mu.Unlock()
	if len(bad) > 0 {
		t.Errorf("the driver signalled what is not its unreaped child: %v", bad)
	}
	if processGone(decoy.Process.Pid) {
		t.Error("a process outside the sandbox went with it")
	}
}

// gated is a sandbox whose guest's words stop reaching the supervisor
// once the supervisor has sent start: the start's answer never comes. With
// stall, the guest's credit stops too, so the start's write waits.
type gated struct {
	sandbox
	sent  chan struct{}
	stall bool
}

func (g *gated) Start(ctx context.Context) (net.Conn, error) {
	c, err := g.sandbox.Start(ctx)
	if err != nil {
		return nil, err
	}
	return &gateConn{Conn: c, sent: g.sent, stall: g.stall, closed: make(chan struct{})}, nil
}

// gateConn drops what it reads after it wrote start, or with stall reads
// nothing more until it closes.
type gateConn struct {
	net.Conn
	sent   chan struct{}
	stall  bool
	once   sync.Once
	closed chan struct{}
	shut   sync.Once
}

func (c *gateConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"op":"start"`)) {
		c.once.Do(func() { close(c.sent) })
	}
	return c.Conn.Write(p)
}

func (c *gateConn) Read(p []byte) (int, error) {
	for {
		select {
		case <-c.sent:
			if c.stall {
				<-c.closed
				return 0, net.ErrClosed
			}
		default:
		}
		n, err := c.Conn.Read(p)
		select {
		case <-c.sent:
			if err != nil {
				return 0, err
			}
		default:
			return n, err
		}
	}
}

func (c *gateConn) Close() error {
	c.shut.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// gate makes each sandbox of the driver a gated one, and returns the
// channel that closes when the supervisor sends start, and the driver.
func gate(t *testing.T, stall bool) (sent chan struct{}, drivers <-chan *plain) {
	t.Helper()
	sent = make(chan struct{})
	got := make(chan *plain, 4)
	old := sandboxFor
	sandboxFor = func(m *host.Manifest) (sandbox, error) {
		sb, err := newSandbox(m)
		if p, ok := sb.(*plain); ok {
			got <- p
		}
		return &gated{sandbox: sb, sent: sent, stall: stall}, err
	}
	t.Cleanup(func() { sandboxFor = old })
	return sent, got
}

// A boot that ends before the start's answer, by a stop with the host or
// by its deadline, leaves no process of the sandbox: not the agent, nor a
// child of it, though the guest started them and they ignore SIGHUP and
// SIGTERM. The guest init is reaped, and the record says how the boot
// ended.
func TestAnInterruptedBootLeavesNoProcess(t *testing.T) {
	for _, c := range []struct {
		name string
		stop bool
		kind string
	}{
		{"a stop with the host", true, host.EndSystem},
		{"the boot's deadline", false, host.EndBoot},
	} {
		t.Run(c.name, func(t *testing.T) {
			signals := fakeSignals(t)
			old := bootTimeout
			bootTimeout = 4 * time.Second
			t.Cleanup(func() { bootTimeout = old })
			id, pids := plainSession(t)
			sent, drivers := gate(t, false)
			ended := make(chan error, 1)
			go func() { ended <- Run(id) }()
			sig := signals()
			select {
			case <-sent:
			case <-time.After(10 * time.Second):
				t.Fatal("no start")
			}
			agent, child := pids()
			if c.stop {
				sig <- syscall.SIGTERM
			}
			var err error
			select {
			case err = <-ended:
			case <-time.After(20 * time.Second):
				t.Fatal("the boot did not end")
			}
			goneWithin(t, 10*time.Second, agent, child)
			reaped(t, (<-drivers).cmd.Process.Pid)
			m, _ := host.LoadManifest(id)
			if err == nil || m.Life == nil || m.Life.End == nil || m.Life.End.Kind != c.kind || !m.Life.Started.IsZero() {
				t.Errorf("after the boot: %v, %+v", err, m.Life)
			}
		})
	}
}

// A stop with the host ends a start's write that waits for the guest's
// credit at once: the boot ends long before its deadline, and the guest
// init is reaped.
func TestAStopEndsAStartWriteThatWaits(t *testing.T) {
	signals := fakeSignals(t)
	id, _ := plainSession(t)
	m, _ := host.LoadManifest(id)
	m.Env = append(m.Env, "BIG="+strings.Repeat("x", 3*link.DefaultWindow))
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	sent, drivers := gate(t, true)
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	sig := signals()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("no start")
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	sig <- syscall.SIGTERM
	select {
	case err := <-ended:
		if !errors.Is(err, errStopped) || time.Since(start) > 15*time.Second {
			t.Errorf("a stop during the start's write = %v, after %s", err, time.Since(start))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stop did not end the start's write")
	}
	reaped(t, (<-drivers).cmd.Process.Pid)
}

// A stop with the host ends the wait for the session's secrets at once,
// and no sandbox starts after it.
func TestAStopEndsTheWaitForSecrets(t *testing.T) {
	signals := fakeSignals(t)
	id, _ := plainSession(t)
	m, _ := host.LoadManifest(id)
	m.Secrets = []string{"TEST_TOKEN"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	var started atomic.Bool
	old := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) {
		return &startSpy{started: &started}, nil
	}
	t.Cleanup(func() { sandboxFor = old })
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	sig := signals()
	waitFor(t, "the session's socket", func() bool {
		_, err := os.Stat(paths.Socket(id))
		return err == nil
	})
	start := time.Now()
	sig <- syscall.SIGTERM
	select {
	case err := <-ended:
		if !errors.Is(err, errStopped) || time.Since(start) > 5*time.Second {
			t.Errorf("a stop during the wait for secrets = %v, after %s", err, time.Since(start))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stop did not end the wait for secrets")
	}
	if started.Load() {
		t.Error("a sandbox started after the stop")
	}
}

// startSpy is a sandbox that says whether it started.
type startSpy struct{ started *atomic.Bool }

func (s *startSpy) Start(context.Context) (net.Conn, error) {
	s.started.Store(true)
	return nil, errors.New("a spy starts nothing")
}
func (s *startSpy) Kill() error { return nil }
func (s *startSpy) Wait() error { return nil }
