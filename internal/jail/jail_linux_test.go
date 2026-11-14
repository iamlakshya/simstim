package jail

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// testMode names how a test builds its jails: with namespaces, or with
// Landlock and seccomp alone, as on Ubuntu before the root step.
type testMode struct {
	name   string
	userns bool
}

var bothModes = []testMode{{"namespaces", true}, {"landlock", false}}

// inMode makes Command build jails in m for the rest of the test, or skips
// the test where this host cannot. The tests' own probe decides, not
// Userns, and a Userns that disagrees with it fails the test.
func inMode(t *testing.T, m testMode) {
	t.Helper()
	works := namespacesWork(t)
	if works != Userns() {
		t.Fatalf("Userns says %v, but the tests' own probe says %v", Userns(), works)
	}
	if m.userns && !works {
		t.Skip("no user namespace here lets a jail mount a tmpfs and a proc. Run as a normal user " +
			"on a host that allows user namespaces, or in a container with --privileged")
	}
	if !m.userns && kernelABI() < minABI {
		t.Skipf("this kernel's Landlock ABI is %d: a jail without a user namespace needs ABI 3", kernelABI())
	}
	old := usernsWorks
	usernsWorks = func() (bool, error) { return m.userns, nil }
	t.Cleanup(func() { usernsWorks = old })
}

// home gives this test a populated home that every jail must hide.
func home(t *testing.T) string {
	t.Helper()
	return fixtureHome(t, true)
}

// TestTheFourLimits checks the acceptance of P1.3: a jailed command cannot
// open $HOME, connect a TCP socket with NetNone, call ptrace on its parent
// or on another process, or read another process's memory. The target is
// dumpable and lets any process trace it, so only the jail refuses. Each
// mode must hold all four: without a user namespace, Landlock and seccomp
// hold them alone.
func TestTheFourLimits(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			in := probeIn{Home: home(t), Addr: ln.Addr().String(), Pid: startTarget(t)}

			// Unjailed, the probe reaches all four.
			control := unjailed(t, in)
			free, _, _ := start(t, control, in)
			_ = control.Wait()
			if free.Home != "ok" || free.TCP != "ok" || free.Ptrace != "ok" || free.Mem != "ok" {
				t.Fatalf("unjailed, the probe could not reach what the jail must hide: %+v", free)
			}

			in.FD3 = true
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			exe := testExe(t)
			cmd, err := Command(t.Context(), Spec{Argv: probeArgs(t, exe, in), Binds: probeBinds(exe), Net: NetNone,
				Seccomp: ProfileConverter, Env: probeEnv, Files: []*os.File{w}})
			if err != nil {
				t.Fatal(err)
			}
			out, _, stderr := start(t, cmd, in)
			_ = w.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("the jailed probe: %v\n%s", err, stderr)
			}
			for what, got := range map[string]string{"open $HOME": out.Home, "connect over TCP": out.TCP,
				"trace another process": out.Ptrace, "trace its parent": out.Parent, "open another process's memory": out.Mem} {
				if got == "ok" {
					t.Errorf("the jailed command could %s", what)
				} else {
					t.Logf("%s: %s", what, got)
				}
			}
			if b, _ := io.ReadAll(r); string(b) != "fd3" {
				t.Errorf("fd 3 got %q, want the probe's fd3", b)
			}
			if !slices.Equal(out.Env, probeEnv) {
				t.Errorf("the command's environment is %q, want Spec.Env %q", out.Env, probeEnv)
			}
			// In a PID namespace, the session leader is the jail's init, PID
			// 1, or outside it, 0.
			if sid, _ := unix.Getsid(0); (m.userns && out.Sid > 1) || (!m.userns && out.Sid == sid) {
				t.Errorf("the command is in session %d, and the test in %d: want a new one", out.Sid, sid)
			}
		})
	}
}

// TestVMMJailKeepsGroups checks that a vmm jail never calls setgroups, so
// it keeps the login user's groups, such as kvm (spec 14.4.2). The jail has
// as many groups as the test, and as the subuid, which owns nothing, it
// reads a fixture that only one supplementary group may read.
func TestVMMJailKeepsGroups(t *testing.T) {
	groups, err := unix.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	group := -1
	for _, g := range groups {
		if g != os.Getgid() {
			group = g
		}
	}
	if group < 0 {
		t.Skip("this user has no supplementary group besides its primary one")
	}
	dir := filepath.Join(t.TempDir(), "open")
	exe := copyExe(t, dir)
	fixture := filepath.Join(dir, "fixture")
	if err := os.WriteFile(fixture, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(fixture, -1, group); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture, 0o040); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		m   testMode
		sub bool
	}{{bothModes[0], false}, {bothModes[1], false}, {bothModes[0], true}}
	for _, c := range cases {
		t.Run(c.m.name+"/subuid="+strconv.FormatBool(c.sub), func(t *testing.T) {
			inMode(t, c.m)
			if _, ok := lookupSubID(); c.sub && !ok {
				t.Skip("this user has no subuid range, or no newuidmap")
			}
			in := probeIn{Read: []string{fixture}}
			out := jailed(t, Spec{Argv: probeArgs(t, exe, in), Binds: append(libBinds(), Bind{Path: dir}),
				Subuid: c.sub, Seccomp: ProfileVMM}, in)
			// The file's owner reads with the owner's bits, so only the
			// subuid, which is not its owner, reads it through the group.
			if c.sub && out.Read[fixture] != "ok" {
				t.Errorf("the subuid could not read a file of its group %d: %s (groups %v)", group, out.Read[fixture], out.Groups)
			}
			if !c.m.userns && !slices.Equal(out.Groups, groups) {
				t.Errorf("the jail has groups %v, want %v", out.Groups, groups)
			}
			if len(out.Groups) != len(groups) {
				t.Errorf("the jail has groups %v, want as many as %v", out.Groups, groups)
			}
		})
	}
}

// TestCommandRefusesOldLandlockWithoutUserns fakes a Landlock ABI below 3
// on a host without user namespaces: Command refuses the jail.
func TestCommandRefusesOldLandlockWithoutUserns(t *testing.T) {
	oldNS, oldABI := usernsWorks, landlockABI
	t.Cleanup(func() { usernsWorks, landlockABI = oldNS, oldABI })
	usernsWorks = func() (bool, error) { return false, nil }
	exe := testExe(t)
	s := Spec{Argv: []string{exe}, Binds: []Bind{{Path: exe}}, Seccomp: ProfileConverter}
	for abi := range minABI {
		landlockABI = func() int { return abi }
		cmd, err := Command(t.Context(), s)
		if cmd != nil || err == nil || !strings.Contains(err.Error(), "Landlock ABI 3") || hintOf(err) == "" {
			t.Errorf("ABI %d: Command = %v, %v; want a refusal with a hint", abi, cmd, err)
		}
	}
	landlockABI = func() int { return minABI }
	if _, err := Command(t.Context(), s); err != nil {
		t.Errorf("ABI 3: %v", err)
	}
}

// TestUsernsAgreesWithTheTestsOwnProbe checks Userns both ways: false where
// no user namespace with a mount works, as in a container or on Ubuntu
// 23.10 or later before the root step, and true where one does.
func TestUsernsAgreesWithTheTestsOwnProbe(t *testing.T) {
	works := namespacesWork(t)
	t.Logf("a user namespace works: %v; Userns: %v; Landlock ABI %d", works, Userns(), kernelABI())
	if works != Userns() {
		t.Errorf("Userns says %v, but a user namespace with a tmpfs and a proc works: %v", Userns(), works)
	}
}

// TestUsernsKeepsOnlyClearAnswers checks that a probe that times out, or
// finds the host short of memory, gives Command an error, and runs again.
func TestUsernsKeepsOnlyClearAnswers(t *testing.T) {
	probeMu.Lock()
	oldProbe, oldProbed := probeFn, probed
	probed = nil
	probeMu.Unlock()
	t.Cleanup(func() {
		probeMu.Lock()
		probeFn, probed = oldProbe, oldProbed
		probeMu.Unlock()
	})
	calls := 0
	probeFn = func() (bool, error) {
		calls++
		if calls == 1 {
			return false, failure("the check for user namespaces timed out", "Try again.")
		}
		return true, nil
	}
	exe := testExe(t)
	if _, err := Command(t.Context(), Spec{Argv: []string{exe}, Binds: []Bind{{Path: exe}}, Seccomp: ProfileConverter}); err == nil ||
		!strings.Contains(err.Error(), "timed out") {
		t.Errorf("Command after a probe that timed out = %v, want its error", err)
	}
	if ok, err := usernsState(); !ok || err != nil || calls != 2 {
		t.Errorf("the next probe = %v, %v after %d calls; want true from a new probe", ok, err, calls)
	}
	if ok, _ := usernsState(); !ok || calls != 2 {
		t.Errorf("a clear answer was not kept: %d calls", calls)
	}
}

func TestCommandNeedsInit(t *testing.T) {
	initDone.Store(false)
	defer initDone.Store(true)
	if Userns() {
		t.Error("Userns without Init is true")
	}
	if _, err := Command(t.Context(), Spec{}); err == nil || hintOf(err) == "" {
		t.Errorf("Command without Init = %v, want a refusal with a hint", err)
	}
}

// TestSetupFailureExits125 checks that a jail whose command cannot run
// exits with 125, and says why and what to try.
func TestSetupFailureExits125(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			cmd, err := Command(t.Context(), Spec{Argv: []string{dir + "/no-such-program"},
				Binds: append(libBinds(), Bind{Path: dir}), Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			var ee *ExitError
			if !errors.As(err, &ee) || ee.Code != setupFailed || cmd.ExitCode() != setupFailed {
				t.Fatalf("exit = %v, want %d\n%s", err, setupFailed, out)
			}
			if !strings.Contains(string(out), "Error: cannot run") || !strings.Contains(string(out), "Try: ") {
				t.Errorf("stderr = %q, want the reason and a Try: line", out)
			}
		})
	}
}

// TestNamespacesShowOnlyTheBinds checks that a jail with a user namespace
// shows no host path but its binds: not $XDG_RUNTIME_DIR, /dev/vhost-* or
// the home folder (spec 5.6). Its root is read-only.
func TestNamespacesShowOnlyTheBinds(t *testing.T) {
	inMode(t, testMode{"namespaces", true})
	root := t.TempDir()
	exe := copyExe(t, filepath.Join(root, "bound"))
	sibling := filepath.Join(root, "unbound")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(sibling, "secret")
	if err := os.WriteFile(secret, []byte("host fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	hidden := []string{"/etc/passwd", "/home", "/root", "/run", "/var", "/dev/vhost-net", "/dev/vhost-vsock", "/dev/kvm"}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		hidden = append(hidden, x)
	}
	hidden = append(hidden, root, sibling, secret)
	in := probeIn{Stat: append(hidden, "/usr", "/proc/self"), Write: []string{"/made"}}
	control := unjailed(t, in)
	free, _, _ := start(t, control, in)
	if err := control.Wait(); err != nil || free.Stat[sibling] != "ok" || free.Stat[secret] != "ok" {
		t.Fatalf("the unjailed probe cannot see its host fixtures: %v, %+v", err, free.Stat)
	}
	binds := probeBinds(exe)
	out := jailed(t, Spec{Argv: probeArgs(t, exe, in), Binds: binds, Seccomp: ProfileVMM}, in)
	for _, issue := range namespaceVisibilityIssues(hidden, binds, out) {
		t.Error(issue)
	}
	for _, p := range []string{"/usr", "/proc/self"} {
		if out.Stat[p] != "ok" {
			t.Errorf("the jail does not show %s: %s", p, out.Stat[p])
		}
	}
	if out.Write["/made"] == "ok" {
		t.Error("the jail could write to its root")
	}
}

// TestBindsAreReadOnlyUnlessWrite checks that only a bind with Write takes
// writes, in both modes.
func TestBindsAreReadOnlyUnlessWrite(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			ro, rw := t.TempDir(), t.TempDir()
			out := jailed(t, Spec{Binds: []Bind{{Path: ro}, {Path: rw, Write: true}}, Seccomp: ProfileConverter},
				probeIn{Write: []string{ro + "/f", rw + "/f"}})
			if out.Write[ro+"/f"] == "ok" {
				t.Error("the jail wrote to a read-only bind")
			}
			if out.Write[rw+"/f"] != "ok" {
				t.Errorf("the jail could not write to a writable bind: %s", out.Write[rw+"/f"])
			}
		})
	}
}

// TestEgressReachesTheNetwork checks that NetHost and the egress profile
// let a jail connect over TCP, while NetNone blocks it even with the egress
// profile.
func TestEgressReachesTheNetwork(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			in := probeIn{Addr: ln.Addr().String()}
			if out := jailed(t, Spec{Net: NetHost, Seccomp: ProfileEgress}, in); out.TCP != "ok" {
				t.Errorf("the egress jail could not connect: %s", out.TCP)
			}
			if blocked := jailed(t, Spec{Net: NetNone, Seccomp: ProfileEgress}, in); blocked.TCP == "ok" {
				t.Error("the egress jail connected over TCP with NetNone")
			}
		})
	}
}

// inUmask runs f with the process's umask set to mask.
func inUmask(mask int, f func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	f()
}
