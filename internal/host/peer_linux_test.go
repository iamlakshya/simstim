package host

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// peerListener listens on a Unix socket in a new folder, and returns it
// with its path.
func peerListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, sock
}

// TestPeerOfReadsTheRealPeer reads the peer of a real connection: this
// process, of this user, alive, and at the place that its own cgroup file
// gives.
func TestPeerOfReadsTheRealPeer(t *testing.T) {
	ln, sock := peerListener(t)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	p, err := peerOf(srv)
	if err != nil || p.pid != os.Getpid() || p.uid != os.Getuid() {
		t.Fatalf("peer %+v, %v; want pid %d, uid %d", p, err, os.Getpid(), os.Getuid())
	}
	if p.alive == nil {
		t.Log("this kernel gives no pidfd: it is older than Linux 6.5")
	} else {
		if !p.alive() {
			t.Error("the pidfd says this process has gone")
		}
		p.done()
	}
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: "abcd2345", peerCheck: true}
	if got, want := h.placeOf(srv), placeIn(self, UnitName("abcd2345")); got != want {
		t.Errorf("this test's process is at place %d, and its cgroup file says %d", got, want)
	}
}

// TestAPeerThatLeftCannotPass connects from a child process, a hook, that
// then exits. Its pid is free for another process to take, so neither its
// pidfd nor a new read of the peer may let it pass.
func TestAPeerThatLeftCannotPass(t *testing.T) {
	ln, sock := peerListener(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary acts as the helper's hook (hosttest.Main). It dials
	// sock, says hello, and waits a second at most for the answer.
	cmd := exec.Command(exe, "hook", "stop")
	cmd.Env = append(os.Environ(), brand.Env("HOOK_SOCK")+"="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	p, err := peerOf(srv)
	if err != nil || p.pid != cmd.Process.Pid {
		t.Fatalf("peer %+v, %v; want pid %d", p, err, cmd.Process.Pid)
	}
	if p.alive == nil {
		_ = cmd.Wait()
		t.Skip("this kernel gives no pidfd: it is older than Linux 6.5")
	}
	defer p.done()
	if !p.alive() {
		t.Fatal("the pidfd says the hook has gone while it waits")
	}
	srv.Close() // the hook reads the end, and exits
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook did not exit")
	}
	if p.alive() {
		t.Error("the pidfd says a reaped process lives")
	}
}

// TestAPeerGoneBeforeTheCheckCannotPass reads the peer only after it has
// exited. A kernel with pidfds then gives none, and the read fails, or a
// newer one gives a pidfd that says the process has gone.
func TestAPeerGoneBeforeTheCheckCannotPass(t *testing.T) {
	ln, sock := peerListener(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "hook", "stop")
	cmd.Env = append(os.Environ(), brand.Env("HOOK_SOCK")+"="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	// The hook gives up after a second without an answer, and exits.
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if p, err := peerOf(srv); err == nil && p.alive != nil {
		if p.alive() {
			t.Error("a pidfd says a peer that has gone lives")
		}
		p.done()
	}
	h := &Holder{id: "abcd2345", peerCheck: true}
	if place := h.placeOf(srv); place != placeInside {
		t.Errorf("a peer that has gone is at place %d, want inside", place)
	}
}

// zombieConnector connects to the socket that its argument names, forks a
// child that keeps the socket open, prints the child's pid, and exits. The
// test reaps it only at its end, so it stays a zombie until then.
const zombieConnector = `
import os, socket, sys, time
s = socket.socket(socket.AF_UNIX)
s.connect(sys.argv[1])
pid = os.fork()
if pid == 0:
    time.sleep(30)
    os._exit(0)
print(pid, flush=True)
os._exit(0)
`

// TestAnUnreapedConnectorCannotPassOnCgroupV1 is the case of a connector
// that exits without being reaped, and leaves its socket to another process
// of its session. The pidfd still says that the zombie lives, and cgroup v1
// shows "/" for it, which is no place at all. The peer counts as inside.
func TestAnUnreapedConnectorCannotPassOnCgroupV1(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3")
	}
	ln, sock := peerListener(t)
	cmd := exec.Command(python, "-I", "-c", zombieConnector, sock)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(line))
	defer func() {
		if child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	}()
	// The connector has exited, and stays a zombie until the Wait above.
	waitUntil(t, func() bool {
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat")
		f := strings.Fields(string(b))
		return len(f) > 2 && f[2] == "Z"
	})
	if p, err := peerOf(srv); err == nil && p.alive != nil {
		t.Logf("the pidfd of the zombie says it lives: %v", p.alive())
		p.done()
	}
	// What cgroup v1 shows for the zombie: roots only, whatever its group,
	// alone or beside an unused v2 hierarchy.
	for _, file := range []string{"12:memory:/\n4:pids:/\n1:name=systemd:/\n", "12:memory:/\n1:name=systemd:/\n0::/\n"} {
		fakeProc(t, map[string]string{strconv.Itoa(cmd.Process.Pid) + "/cgroup": file})
		h := &Holder{id: "abcd2345", peerCheck: true}
		if place := h.placeOf(srv); place != placeInside {
			t.Errorf("the zombie connector with %q is at place %d, want inside", file, place)
		}
	}
}
