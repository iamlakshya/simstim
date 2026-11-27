package jail

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The test binary is also the command that tests run in a jail. Started
// with one of these arguments, it is: the probe, which tries what a jail
// must refuse and prints what happened; the tests' own check that a user
// namespace works; a process that the probe may trace; and a process with
// a terminal, which runs a jail on it.
const (
	probeArg   = "jail-probe"
	nsProbeArg = "jail-ns-probe"
	targetArg  = "jail-ptrace-target"
	ttyArg     = "jail-tty-host"
	restArg    = "jail-rest-probe"
	holdArg    = "jail-hold-inherited-file"
)

func TestMain(m *testing.M) {
	childrenTestHooks()
	if len(os.Args) > 0 && os.Args[0] == argv0 {
		testHooks(os.Getenv("JAILTEST_FDFAIL"), os.Getenv("JAILTEST_STALL") == "1")
		pivotHook(os.Getenv("JAILTEST_PIVOT"))
	}
	if os.Getenv("JAILTEST_GC") == "1" {
		stageHook = func() {
			for {
				runtime.GC()
				time.Sleep(time.Millisecond)
			}
		}
	}
	Init()
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case probeArg:
			os.Exit(runProbe(os.Args[2]))
		case nsProbeArg:
			os.Exit(nsProbe())
		case targetArg:
			os.Exit(ptraceTarget())
		case ttyArg:
			os.Exit(ttyHost(os.Args[2]))
		case restArg:
			os.Exit(restProbe(os.Args[2]))
		case holdArg:
			os.Exit(holdInheritedFile())
		}
	}
	os.Exit(m.Run())
}

// probeIn says what the probe tries.
type probeIn struct {
	Home    string      // a folder to open, the user's home
	Addr    string      // a TCP address to connect to
	Pid     int         // a process to trace and whose memory to open
	Limits  int         // an owned outside process whose resource limits to change
	FD3     bool        // write "fd3" to fd 3
	FD50    bool        // write "LEAKED" to fd 50
	STI     string      // push this into the terminal on stdin with TIOCSTI
	Setsid  bool        // try to start a new session
	WriteTo string      // make a file in this folder
	Stat    []string    // paths to look up
	Write   []string    // files to make
	Read    []string    // files to read
	Mounts  []string    // paths whose actual mount flags to report
	Meta    string      // a file whose mode, owner, times and xattrs to change
	Moves   [][2]string // files to rename, from and to
	Links   [][2]string // hard links to make, from and to
	Wait    bool        // after the answer, wait until stdin closes
}

// probeOut is what the probe found. Each check is "ok" or its error.
type probeOut struct {
	Home, TCP, Ptrace, Mem, Parent string
	Limits                         string
	FD50, STI, Setsid              string
	Stat, Write, Read, Meta        map[string]string
	Moves, Links                   []string
	Groups                         []int
	Mounts                         map[string]int64
	UID, Sid, Args                 int
	Env                            []string
}

func runProbe(arg string) int {
	var in probeIn
	if err := json.Unmarshal([]byte(arg), &in); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	out := probeOut{UID: os.Getuid(), Args: len(os.Args), Env: os.Environ(),
		Stat: map[string]string{}, Write: map[string]string{}, Read: map[string]string{}}
	out.Sid, _ = unix.Getsid(0)
	out.Groups, _ = unix.Getgroups()
	out.Home = result(func() error {
		f, err := os.Open(in.Home)
		if err == nil {
			_, err = f.Readdirnames(1)
			_ = f.Close()
			if errors.Is(err, io.EOF) {
				err = nil // an empty directory is still readable
			}
		}
		return err
	})
	out.TCP = result(func() error {
		c, err := net.DialTimeout("tcp", in.Addr, 3*time.Second)
		if err == nil {
			_ = c.Close()
		}
		return err
	})
	out.Mem = result(func() error {
		f, err := os.Open("/proc/" + strconv.Itoa(in.Pid) + "/mem")
		if err == nil {
			_ = f.Close()
		}
		return err
	})
	if in.Limits > 0 {
		out.Limits = result(func() error {
			return unix.Prlimit(in.Limits, unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 0, Max: 0}, nil)
		})
	}
	for _, p := range in.Stat {
		out.Stat[p] = result(func() error { _, err := os.Lstat(p); return err })
	}
	for _, p := range in.Write {
		out.Write[p] = result(func() error { return os.WriteFile(p, []byte("x"), 0o600) })
	}
	for _, p := range in.Read {
		out.Read[p] = result(func() error { _, err := os.ReadFile(p); return err })
	}
	if len(in.Mounts) > 0 {
		out.Mounts = map[string]int64{}
		for _, path := range in.Mounts {
			var st unix.Statfs_t
			if err := unix.Statfs(path, &st); err == nil {
				out.Mounts[path] = st.Flags
			}
		}
	}
	if p := in.Meta; p != "" {
		out.Meta = map[string]string{
			"chmod":     result(func() error { return unix.Chmod(p, 0o777) }),
			"chown":     result(func() error { return unix.Chown(p, os.Getuid(), os.Getgid()) }),
			"utimensat": result(func() error { return unix.UtimesNanoAt(unix.AT_FDCWD, p, make([]unix.Timespec, 2), 0) }),
			"setxattr":  result(func() error { return unix.Setxattr(p, "user.jail", []byte("x"), 0) }),
		}
	}
	for _, m := range in.Moves {
		out.Moves = append(out.Moves, result(func() error { return os.Rename(m[0], m[1]) }))
	}
	for _, l := range in.Links {
		out.Links = append(out.Links, result(func() error { return os.Link(l[0], l[1]) }))
	}
	if in.FD3 {
		f := os.NewFile(3, "fd3")
		_, _ = f.WriteString("fd3")
		_ = f.Close()
	}
	if in.FD50 {
		out.FD50 = result(func() error { _, err := unix.Write(50, []byte("LEAKED")); return err })
	}
	if in.STI != "" {
		out.STI = result(func() error { return sti(0, in.STI) })
	}
	if in.Setsid {
		out.Setsid = result(func() error { _, err := unix.Setsid(); return err })
	}
	if in.WriteTo != "" {
		_ = os.WriteFile(filepath.Join(in.WriteTo, "made"), []byte("x"), 0o644)
	}
	// Last: a successful seize holds the traced process until this exits.
	out.Ptrace = result(func() error { return unix.PtraceSeize(in.Pid) })
	out.Parent = result(func() error { return unix.PtraceSeize(os.Getppid()) })
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	if in.Wait {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	return 0
}

// sti pushes s into the terminal on fd as if typed.
func sti(fd int, s string) error {
	for i := range len(s) {
		b := s[i]
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCSTI, uintptr(unsafe.Pointer(&b))); e != 0 {
			return e
		}
	}
	return nil
}

func result(f func() error) string {
	if err := f(); err != nil {
		return err.Error()
	}
	return "ok"
}

// ptraceTarget is a dumpable process that lets any process trace it, even
// under Yama, and waits until stdin closes.
func ptraceTarget() int {
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// startTarget starts a ptrace target and returns its process ID.
func startTarget(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), self, targetArg)
	cmd.Env = probeEnv
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("the ptrace target: %q, %v", line, err)
	}
	return cmd.Process.Pid
}

// nsProbe is the tests' own check of a user namespace: mapped to root in a
// new user, mount and PID namespace, it mounts a tmpfs and a proc.
func nsProbe() int {
	steps := []func() error{
		func() error { return unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "") },
		func() error { return unix.Mount("tmpfs", "/tmp", "tmpfs", 0, "") },
		func() error { return unix.Mkdir("/tmp/p", 0o700) },
		func() error { return unix.Mount("proc", "/tmp/p", "proc", 0, "") },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return 1
		}
	}
	return 0
}

// namespacesWork says whether a jail's user namespace works here, found
// out by making one and mounting a tmpfs and a proc in it, as root in it,
// not by reading a sysctl. It is independent of Userns.
func namespacesWork(t *testing.T) bool {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), self, nsProbeArg)
	cmd.Env = probeEnv
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
	}
	return cmd.Run() == nil
}

// probeEnv is the jailed probe's whole environment. The race detector
// waits a second at exit unless told not to.
var probeEnv = []string{"GORACE=atexit_sleep_ms=0"}

// libBinds are what a dynamically linked program needs, /dev/null and
// /dev/urandom.
func libBinds() []Bind {
	binds := []Bind{{Path: "/dev/null", Write: true}, {Path: "/dev/urandom"}}
	for _, p := range []string{"/usr", "/bin", "/lib", "/lib64", "/lib32", "/etc/ld.so.cache"} {
		if _, err := os.Stat(p); err == nil {
			binds = append(binds, Bind{Path: p})
		}
	}
	return binds
}

// probeBinds are the paths that the probe needs: the test binary and the
// C library that a race-enabled build loads.
func probeBinds(exe string) []Bind {
	return append(libBinds(), Bind{Path: exe})
}

// start starts the jailed or plain probe and returns its answer. With
// in.Wait, the probe waits until the caller closes stdin.
func start(t *testing.T, c starter, in probeIn) (probeOut, io.WriteCloser, *outFile) {
	t.Helper()
	stderr := newOutFile(t)
	c.setStderr(stderr.f)
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = c.Wait()
		t.Fatalf("the probe gave no answer: %v\n%s", err, stderr.String())
	}
	var out probeOut
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("the probe's answer %q: %v", line, err)
	}
	if !in.Wait {
		_ = stdin.Close()
	}
	return out, stdin, stderr
}

// starter is a *Cmd or an *exec.Cmd.
type starter interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
	setStderr(*os.File)
}

func (c *Cmd) setStderr(f *os.File) { c.Stderr = f }

type plainCmd struct{ *exec.Cmd }

func (c plainCmd) setStderr(f *os.File) { c.Stderr = f }

// probeArgs returns the probe's command line.
func probeArgs(t *testing.T, exe string, in probeIn) []string {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, probeArg, string(b)}
}

// unjailed returns the probe as a plain child of the test.
func unjailed(t *testing.T, in probeIn) plainCmd {
	t.Helper()
	exe := testExe(t)
	cmd := exec.CommandContext(t.Context(), exe, probeArgs(t, exe, in)[1:]...)
	cmd.Env = probeEnv
	return plainCmd{cmd}
}

// jailed runs the probe in a jail of s, with the probe's own binds added,
// and returns its answer.
func jailed(t *testing.T, s Spec, in probeIn) probeOut {
	t.Helper()
	exe := testExe(t)
	if len(s.Argv) == 0 {
		s.Argv = probeArgs(t, exe, in)
		s.Binds = append(probeBinds(exe), s.Binds...)
	}
	s.Env = probeEnv
	cmd, err := Command(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	out, _, stderr := start(t, cmd, in)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the jailed probe: %v\n%s", err, stderr)
	}
	return out
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// openTo makes dir and every folder above it, up to its first parent that
// exists, open to every user: a subuid must pass through them.
func openTo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for d := dir; d != "/" && d != os.TempDir(); d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// copyExe copies the test binary where a subuid can run it.
func copyExe(t *testing.T, dir string) string {
	t.Helper()
	openTo(t, dir)
	b, err := os.ReadFile(testExe(t))
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "probe")
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exe, 0o755); err != nil { // whatever the umask
		t.Fatal(err)
	}
	return exe
}
