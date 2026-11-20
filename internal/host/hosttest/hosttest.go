// Package hosttest runs the host side in tests, on this machine: a host's
// folders in a temporary place, session holders as real child processes,
// and the real bridge in the test's own process. Only tests import it.
//
// The holders are the test binary itself, started again with the helper's
// own arguments ("holder <id>", "hook <event>"): a test binary that uses this
// package calls Main from its TestMain. No systemd is used: holders start
// detached, as they do in a container.
package hosttest

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Main runs the tests, or acts as the host helper when the binary was
// started as one: "holder <id>", "hook <event> [args]" or "keytest". Call it
// from TestMain.
func Main(m *testing.M) {
	if len(os.Args) >= 3 {
		switch os.Args[1] {
		case "holder":
			if err := host.RunHolder(os.Args[2]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "hook":
			host.RunHook(os.Args[2], os.Args[3:])
			os.Exit(0)
		case "rm-as-caller":
			rmAsCaller(os.Args[2:])
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "keytest" {
		if err := host.KeyTest(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	flag.Parse()
	// Child processes write their coverage where this binary writes its
	// own, so `go test -cover` counts the code that holders run too.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		_ = os.Setenv("GOCOVERDIR", f.Value.String())
	}
	os.Exit(m.Run())
}

// Host is one host's folders, in a temporary place that the test owns.
type Host struct {
	// Root holds everything below: the home folder, the runtime folder and
	// the race detector's reports from child processes.
	Root string
	// Home is $HOME. The XDG folders are their defaults under it.
	Home string
	t    testing.TB
}

// New points $HOME, the XDG folders and $SHELL at a new temporary host, for
// the rest of the test, and leaves only the system's programs on $PATH. At
// the end of the test every session is deleted and its holder waited for.
func New(t testing.TB) *Host {
	t.Helper()
	root := shortTempDir(t)
	h := &Host{Root: root, Home: filepath.Join(root, "home"), t: t}
	run := filepath.Join(root, "run")
	for _, d := range []string{h.Home, run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", h.Home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h.Home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h.Home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(h.Home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h.Home, ".cache"))
	t.Setenv("XDG_RUNTIME_DIR", run)
	// No systemd user manager can be reached from here, so holders start
	// as detached processes.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("SHELL", "/bin/sh")
	// No agent the developer installed is found, or run, by a test. A test
	// that needs a program of its own puts it in a folder of its own.
	t.Setenv("PATH", systemPath())
	// A data race in a holder is written here, and fails the test. The
	// race detector's pause at exit would slow every holder down.
	t.Setenv("GORACE", "log_path="+filepath.Join(root, "race")+" atexit_sleep_ms=0")
	// Registered before the cleanup below, so it is undone after it: the
	// cleanup deletes the sessions from outside every session too.
	outside(t)
	t.Cleanup(h.cleanup)
	return h
}

// rmAsCaller is a test's stand-in for a program that runs the helper's rm:
// "rm-as-caller <id> <manager namespace or none>". It prints its own view of
// its cgroup, waits for a line on stdin, then deletes session id as rm does,
// with its real cgroup and namespace, and the user manager's namespace from
// its argument. It prints the result, and exits.
func rmAsCaller(args []string) {
	if len(args) != 2 {
		os.Exit(2)
	}
	manager := args[1]
	if manager == "none" {
		manager = ""
	}
	host.CgroupNamespaces = func() (string, string, error) {
		self, err := os.Readlink("/proc/self/ns/cgroup")
		return self, manager, err
	}
	self, _ := os.ReadFile("/proc/self/cgroup")
	fmt.Printf("SELF %q\n", strings.TrimSpace(string(self)))
	_, _ = io.ReadAll(io.LimitReader(os.Stdin, 1))
	if _, err := host.Delete(args[0]); err != nil {
		fmt.Printf("RESULT %v\n", err)
	} else {
		fmt.Println("RESULT deleted")
	}
	os.Exit(0)
}

// outside makes this test's process look, to the helper's own check
// (host.CheckCaller), like a login shell outside every session, on a host
// with a systemd user manager, until the test ends. So the tests pass even
// when they run inside a session's unit. InSession says otherwise.
func outside(t testing.TB) {
	uid := os.Getuid()
	cgroup := fmt.Sprintf("0::/user.slice/user-%d.slice/session-1.scope\n", uid)
	self, ns := host.SelfCgroup, host.CgroupNamespaces
	host.SelfCgroup = func() ([]byte, error) { return []byte(cgroup), nil }
	host.CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
	t.Cleanup(func() { host.SelfCgroup, host.CgroupNamespaces = self, ns })
}

// systemPath is a $PATH of the system's folders, and of the folders of the
// programs that tests run, wherever this machine keeps them.
func systemPath() string {
	dirs := []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	for _, name := range []string{"git", "infocmp", "tic", "stty"} {
		if p, err := exec.LookPath(name); err == nil && !slices.Contains(dirs, filepath.Dir(p)) {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// shortTempDir makes a temporary folder with a short path: a socket's path
// must fit in about 100 bytes, and t.TempDir's names are long.
func shortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ht")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir) > 60 {
		_ = os.RemoveAll(dir)
		if dir, err = os.MkdirTemp("/tmp", "ht"); err != nil {
			t.Fatal(err)
		}
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Install puts a copy of the test binary where the client installs the
// helper, under version, makes it the current one, and returns its path.
// With vt, the shadow terminal goes next to it, as the client installs it.
func (h *Host) Install(version, vt string) string {
	h.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	dir := filepath.Join(paths.BinDir(), version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	dst := filepath.Join(dir, brand.HostBinary)
	if err := linkOrCopy(exe, dst); err != nil {
		h.t.Fatal(err)
	}
	if vt != "" {
		if err := linkOrCopy(vt, filepath.Join(dir, brand.VTBinary)); err != nil {
			h.t.Fatal(err)
		}
	}
	cur := filepath.Join(paths.BinDir(), "current")
	_ = os.Remove(cur)
	if err := os.Symlink(version, cur); err != nil {
		h.t.Fatal(err)
	}
	return dst
}

// linkOrCopy makes dst the same file as src: a hard link when it can, so
// that the copy is instant, or else a copy. Never a symbolic link: Linux
// names a process's executable by the link's target.
func linkOrCopy(src, dst string) error {
	_ = os.Remove(dst)
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// HolderLog returns the log of a session's holder.
func (h *Host) HolderLog(id string) string {
	b, _ := os.ReadFile(paths.HolderLog(id))
	return string(b)
}

// cleanup stops every session as `rm` does, and waits for each holder to
// exit, so that none outlives the test and each writes its coverage.
func (h *Host) cleanup() {
	entries, _ := os.ReadDir(paths.SessionsDir())
	for _, e := range entries {
		id := e.Name()
		m, err := host.LoadManifest(id)
		if err != nil {
			continue
		}
		if h.t.Failed() {
			h.t.Logf("holder log of %s:\n%s", id, h.HolderLog(id))
		}
		pid := m.Run.HolderPID
		if p := livePID(id); p > 0 {
			pid = p
		}
		if _, err := host.Delete(id); err != nil {
			h.t.Errorf("delete %s: %v", id, err)
		}
		if err := Reap(pid, 10*time.Second); err != nil {
			h.t.Errorf("session %s: %v", id, err)
		}
	}
	reports, _ := filepath.Glob(filepath.Join(h.Root, "race.*"))
	for _, r := range reports {
		b, _ := os.ReadFile(r)
		h.t.Errorf("a child process found a data race:\n%s", b)
	}
}

// HolderPID asks a session's holder for its process ID. The manifest on
// disk can lag behind: the holder writes it once it has started the agent.
func HolderPID(t testing.TB, id string) int {
	t.Helper()
	pid := livePID(id)
	if pid <= 0 {
		t.Fatalf("the holder of %s did not say its process ID", id)
	}
	return pid
}

// livePID is the process ID that a session's holder reports, or 0.
func livePID(id string) int {
	raw, err := host.HolderRequest(id, "info", nil, 3*time.Second)
	if err != nil {
		return 0
	}
	var m host.Manifest
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	return m.Run.HolderPID
}

// Reap waits until the process pid has exited, and reaps it when it is a
// child of this process. After the timeout it kills the process.
func Reap(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &ws, 0, nil); err == nil || !errors.Is(err, syscall.ECHILD) {
			return
		}
		// Not a child of this process: wait until it is gone.
		for syscall.Kill(pid, 0) == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return fmt.Errorf("process %d did not exit within %s", pid, timeout)
	}
}

// WaitFor polls cond until it is true, or fails the test after timeout.
func WaitFor(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Script writes an executable shell script into a folder of the test's own,
// and returns its path.
func (h *Host) Script(name, body string) string {
	h.t.Helper()
	dir := filepath.Join(h.Root, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(body, "#!") {
		body = "#!/bin/sh\n" + body
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// InSession makes this test's process look, to the helper's own check
// (host.CheckCaller), like a program in the unit of session id, until the
// test ends. It overrides New's outside. Holders that the test starts are
// other processes: they keep their own cgroup.
func InSession(t testing.TB, id string) {
	t.Helper()
	uid := os.Getuid()
	cgroup := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", uid, uid, brand.Slice, host.UnitName(id))
	old := host.SelfCgroup
	host.SelfCgroup = func() ([]byte, error) { return []byte(cgroup), nil }
	t.Cleanup(func() { host.SelfCgroup = old })
}
