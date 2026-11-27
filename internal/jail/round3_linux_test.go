package jail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pivotHook runs in the probe stage of a test: it fakes pivot_root's EINVAL
// on a rootfs root ("rootfs"), or on another root ("other").
func pivotHook(how string) {
	if how == "" {
		return
	}
	pivotRootFn = func(string, string) error { return unix.EINVAL }
	rootfsFn = func() bool { return how == "rootfs" }
}

// TestStdioIsFilesOnly checks that a jail takes only files for its stdio,
// so that it copies nothing: a reader or writer of another type is
// refused, with a hint. A command that writes and exits 7 gives its exit
// error, and its bytes reach the caller's pipes.
func TestStdioIsFilesOnly(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	spec := Spec{Argv: []string{"/bin/sh", "-c", "printf hello; printf oops >&2; exit 7"}, Binds: libBinds(), Seccomp: ProfileConverter}
	for name, set := range map[string]func(*Cmd){
		"Stdin":  func(c *Cmd) { c.Stdin = strings.NewReader("x") },
		"Stdout": func(c *Cmd) { c.Stdout = &bytes.Buffer{} },
		"Stderr": func(c *Cmd) { c.Stderr = &bytes.Buffer{} },
	} {
		cmd, err := Command(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		set(cmd)
		if err := cmd.Start(); err == nil || !strings.Contains(hintOf(err), name+"Pipe") || cmd.Pid() != 0 {
			t.Errorf("Start with a %s that is not a file = %v (hint %q, pid %d), want a refusal before anything starts",
				name, err, hintOf(err), cmd.Pid())
		}
	}
	cmd, err := Command(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdoutPipe(); err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err == nil || hintOf(err) == "" {
		t.Errorf("Start with Stdout and StdoutPipe = %v, want a refusal with a hint", err)
	}

	cmd, err = Command(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outR.Close() }()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = outW
	err = cmd.Start()
	_ = outW.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(stderr)
	err = cmd.Wait()
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 7 || string(out) != "hello" || string(errOut) != "oops" {
		t.Errorf("Wait = %v, stdout %q, stderr %q; want exit 7, hello and oops", err, out, errOut)
	}
}

// TestStartStopsWithAPipeOnStdin cancels a set-up that the stage does not
// read, while the jail's stdin is a pipe whose writer stays open: Start
// must return at the cancel. A reader that is not a file, which a jail
// would have to copy, is refused at once.
func TestStartStopsWithAPipeOnStdin(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	for _, how := range []string{"StdinPipe", "io.Pipe"} {
		ctx, cancel := context.WithCancel(t.Context())
		cmd := stalledJail(t, ctx)
		pr, pw := io.Pipe()
		if how == "StdinPipe" {
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close() }()
		} else {
			cmd.Stdin = pr
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Start() }()
		time.Sleep(300 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err == nil || (how == "StdinPipe" && !errors.Is(err, context.Canceled)) {
				t.Errorf("%s: Start = %v, want an error", how, err)
			}
		case <-time.After(15 * time.Second):
			_ = pw.Close()
			t.Fatalf("%s: Start did not return at the cancel", how)
		}
		_ = pw.Close()
		if pid := cmd.Pid(); pid != 0 {
			if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
				t.Errorf("%s: the stage %d still exists: %v", how, pid, err)
			}
		}
	}
}

// TestASignalAfterTheSetupStopsTheJail sends a signal, and in a second jail
// cancels the context, after the set-up's write ends but before the jail
// moves to running: Start must fail, with the jail killed and reaped.
func TestASignalAfterTheSetupStopsTheJail(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	t.Cleanup(func() { afterSetup = nil })
	for _, how := range []string{"signal", "cancel"} {
		ctx, cancel := context.WithCancel(t.Context())
		cmd, err := Command(ctx, Spec{Argv: []string{"/bin/sleep", "600"}, Binds: libBinds(), Seccomp: ProfileConverter})
		if err != nil {
			t.Fatal(err)
		}
		var serr error
		afterSetup = func(c *Cmd) {
			if how == "signal" {
				serr = c.Signal(syscall.SIGTERM)
			} else {
				cancel()
			}
		}
		err = cmd.Start()
		afterSetup = nil
		if serr != nil {
			t.Errorf("%s: Signal between the write and running = %v", how, serr)
		}
		want := errSignalled
		if how == "cancel" {
			want = context.Canceled
		}
		if err == nil {
			t.Errorf("%s: Start worked, and the jail runs: want %v", how, want)
			cancel()
			_ = cmd.Wait()
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: Start = %v, want %v", how, err, want)
		}
		if err := unix.Kill(cmd.Pid(), 0); !errors.Is(err, unix.ESRCH) {
			t.Errorf("%s: the stage %d still exists: %v", how, cmd.Pid(), err)
		}
		if err := cmd.Wait(); err == nil || hintOf(err) == "" {
			t.Errorf("%s: Wait after a failed Start = %v, want an error with a hint", how, err)
		}
		cancel()
	}
}

// TestExecRulesInAnyOrder gives an Exec rule on a folder and an Exec and
// Write rule on a folder inside it, in both orders, and the inner rule as
// two rules on one path. A bind inside the outer rule's folder must stay
// in sight. Each order must give the same jail.
func TestExecRulesInAnyOrder(t *testing.T) {
	b, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			rw := t.TempDir()
			run, tool, data := rw+"/run", rw+"/run/tool", rw+"/run/data"
			for _, d := range []string{run, tool, data} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, d := range []string{run, tool} {
				if err := os.WriteFile(d+"/t", b, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.Join([]string{
				run + "/t && echo RUNX", tool + "/t && echo TOOLX",
				"touch " + tool + "/w && echo TOOLW", "touch " + run + "/w 2>/dev/null && echo RUNW",
				"touch " + data + "/w && echo DATAW", "touch " + rw + "/w && echo RWW",
			}, "; ")
			orders := map[string][]PathRule{
				"parent first":        {{Path: run, Access: Exec}, {Path: tool, Access: Exec | Write}},
				"child first":         {{Path: tool, Access: Exec | Write}, {Path: run, Access: Exec}},
				"one path, two rules": {{Path: tool, Access: Exec}, {Path: run, Access: Exec}, {Path: tool, Access: Write}},
				"the same, reversed":  {{Path: tool, Access: Write}, {Path: run, Access: Exec}, {Path: tool, Access: Exec}},
			}
			for name, rules := range orders {
				for _, f := range []string{tool + "/w", run + "/w", data + "/w", rw + "/w"} {
					_ = os.Remove(f)
				}
				cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", script},
					Binds: append(libBinds(), Bind{Path: rw, Write: true}, Bind{Path: data, Write: true}),
					Rules: rules, Seccomp: ProfileConverter})
				if err != nil {
					t.Fatal(err)
				}
				out, _ := cmd.CombinedOutput()
				for _, want := range []string{"RUNX", "TOOLX", "TOOLW", "DATAW", "RWW"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("%s: no %s in %q", name, want, out)
					}
				}
				if m.userns && strings.Contains(string(out), "RUNW") {
					t.Errorf("%s: the Exec rule's read-only folder took a write: %q", name, out)
				}
			}
		})
	}
}

// TestAPrivateSubuidBindTwice runs two subuid jails, one after the other,
// with the same writable folder of mode 0700. The first gives it to the
// subuid, so the login user can no longer search it, and the second jail
// must still pin it. Root may search any folder, so this test needs a
// normal user, as in the privileged run as uid 1000.
func TestAPrivateSubuidBindTwice(t *testing.T) {
	subuidOrSkip(t)
	if os.Getuid() == 0 {
		t.Skip("root may search any folder: this test needs a normal user with a subuid range")
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveAll(context.Background(), dir) })
	for i := range 2 {
		n := strconv.Itoa(i + 1)
		cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", "echo " + n + " > " + dir + "/f" + n + " && echo OK" + n},
			Binds: append(libBinds(), Bind{Path: dir, Write: true}), Subuid: true, Seccomp: ProfileConverter})
		if err != nil {
			t.Fatalf("jail %s: %v", n, err)
		}
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "OK"+n) {
			t.Errorf("jail %s: %v\n%s", n, err, out)
		}
		if i == 0 {
			if _, err := os.ReadDir(dir); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("after the first jail, the login user reads the private folder: %v", err)
			}
		}
	}
}

// withProbe runs f with no kept answer of the probe, and with the probe's
// hooks put back afterwards.
func withProbe(t *testing.T, f func()) {
	t.Helper()
	probeMu.Lock()
	oldProbed, oldDir, oldMax := probed, nsDir, maxUserNS
	probed = nil
	probeMu.Unlock()
	defer func() {
		probeMu.Lock()
		probed, nsDir, maxUserNS, probeFlags, probeVars = oldProbed, oldDir, oldMax, 0, nil
		probeMu.Unlock()
	}()
	f()
}

// fakeNSDir returns a folder with a file for each namespace but skip.
func fakeNSDir(t *testing.T, skip string) string {
	t.Helper()
	dir := t.TempDir()
	for _, ns := range []string{"user", "mnt", "pid", "ipc", "uts", "cgroup", "net"} {
		if ns == skip {
			continue
		}
		if err := os.WriteFile(dir+"/"+ns, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEINVALIsARefusalOnlyWhereItMeansOne runs the real probe through
// Command and the kept answer. EINVAL from clone is a clear no only for a
// namespace that the kernel does not know, and EINVAL from pivot_root only
// on a rootfs root. Each other EINVAL gives Command an error with a hint,
// keeps no answer, and the next jail probes again.
func TestEINVALIsARefusalOnlyWhereItMeansOne(t *testing.T) {
	if !namespacesWork(t) {
		t.Skip("this test needs a user namespace with a mount, which this container refuses " +
			"(run it in the privileged container)")
	}
	exe := testExe(t)
	spec := Spec{Argv: []string{exe}, Binds: []Bind{{Path: exe}}, Seccomp: ProfileConverter}
	cases := []struct {
		name    string
		flags   uintptr // CLONE_FS with CLONE_NEWUSER: clone's EINVAL
		missing string  // a namespace that the fake kernel does not know
		pivot   string
		refusal bool
	}{
		{"clone's EINVAL, every namespace known", unix.CLONE_FS, "", "", false},
		{"clone's EINVAL, no cgroup namespace", unix.CLONE_FS, "cgroup", "", true},
		{"pivot_root's EINVAL on a rootfs", 0, "", "rootfs", true},
		{"pivot_root's EINVAL on another root", 0, "", "other", false},
	}
	for _, c := range cases {
		withProbe(t, func() {
			nsDir = fakeNSDir(t, c.missing)
			probeFlags = c.flags
			probeVars = []string{"GORACE=atexit_sleep_ms=0", "JAILTEST_PIVOT=" + c.pivot}
			_, err := Command(t.Context(), spec)
			ok, serr := usernsState()
			probeMu.Lock()
			kept := probed != nil
			probeMu.Unlock()
			switch {
			case c.refusal && (ok || serr != nil || !kept):
				t.Errorf("%s: Userns = %v, %v (kept %v), want a kept no", c.name, ok, serr, kept)
			case c.refusal && err != nil && kernelABI() >= minABI:
				t.Errorf("%s: Command = %v, want a jail without a user namespace", c.name, err)
			case !c.refusal && (err == nil || hintOf(err) == "" || serr == nil || kept):
				t.Errorf("%s: Command = %v, Userns = %v, %v (kept %v), want an error with a hint, and no kept answer",
					c.name, err, ok, serr, kept)
			}
		})
	}
	withProbe(t, func() {
		if ok, err := usernsState(); !ok || err != nil {
			t.Errorf("the real probe after the fakes = %v, %v, want true", ok, err)
		}
	})
}

// TestClassifyProbesEINVAL checks the two EINVALs that are refusals, and
// one that is not, without starting a probe.
func TestClassifyProbesEINVAL(t *testing.T) {
	withProbe(t, func() {
		cloneErr := &fs.PathError{Op: "fork/exec", Err: unix.EINVAL}
		nsDir = fakeNSDir(t, "")
		if ok, err := classifyProbe(cloneErr, false); ok || err == nil || hintOf(err) == "" {
			t.Errorf("clone's EINVAL with every namespace known = %v, %v, want an error with a hint", ok, err)
		}
		nsDir = fakeNSDir(t, "user")
		if ok, err := classifyProbe(cloneErr, false); ok || err != nil {
			t.Errorf("clone's EINVAL without a user namespace = %v, %v, want a clear no", ok, err)
		}
	})
	if got := probeExit(errors.Join(errRootfs, unix.EINVAL)); got != probeRefused {
		t.Errorf("probeExit of pivot_root's EINVAL on a rootfs = %d, want %d", got, probeRefused)
	}
	if got := probeExit(unix.EINVAL); got != probeUnclear {
		t.Errorf("probeExit of another EINVAL = %d, want %d", got, probeUnclear)
	}
}
