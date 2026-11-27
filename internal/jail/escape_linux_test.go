package jail

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// shJail runs sh -c script in a jail with the C library and extra binds,
// and returns its output and exit code.
func shJail(t *testing.T, script string, extra ...Bind) (string, int) {
	t.Helper()
	cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", script},
		Binds: append(libBinds(), extra...), Seccomp: ProfileConverter})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ExitCode()
}

// TestNoDescriptorLeaksIntoAJail puts a pipe at fd 50 without
// close-on-exec, as a descriptor that the helper inherited from its parent:
// no jail may reach it.
func TestNoDescriptorLeaksIntoAJail(t *testing.T) {
	cases := []struct {
		m   testMode
		sub bool
	}{{bothModes[0], false}, {bothModes[1], false}, {bothModes[0], true}}
	for _, c := range cases {
		t.Run(c.m.name+"/subuid="+strconv.FormatBool(c.sub), func(t *testing.T) {
			inMode(t, c.m)
			exe := testExe(t)
			if _, ok := lookupSubID(); c.sub {
				if !ok {
					t.Skip("this user has no subuid range, or no newuidmap")
				}
				exe = copyExe(t, filepath.Join(t.TempDir(), "open"))
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := unix.Dup3(int(w.Fd()), 50, 0); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			in := probeIn{FD50: true}
			out := jailed(t, Spec{Argv: probeArgs(t, exe, in), Binds: probeBinds(exe), Subuid: c.sub, Seccomp: ProfileConverter}, in)
			_ = unix.Close(50)
			b, _ := io.ReadAll(r)
			if out.FD50 == "ok" || strings.Contains(string(b), "LEAKED") {
				t.Errorf("fd 50 of the helper reached the jail: write %s, pipe got %q", out.FD50, b)
			}
		})
	}
}

// ttyResult is what the terminal host saw.
type ttyResult struct {
	Control, Probe, Injected string
}

// ttyHost runs with a terminal of its own: it pushes a line into it, to
// show that this kernel allows TIOCSTI, then runs a jail whose stdin is
// that terminal and which pushes "id" into it, and reports what reached
// the terminal.
func ttyHost(arg string) int {
	var m struct{ Userns bool }
	_ = json.Unmarshal([]byte(arg), &m)
	usernsWorks = func() (bool, error) { return m.Userns, nil }
	report := func(r ttyResult) int {
		b, _ := json.Marshal(r)
		fmt.Println("RESULT " + string(b))
		return 0
	}
	var res ttyResult
	res.Control = result(func() error { return sti(0, "ok\n") })
	if res.Control != "ok" {
		return report(res) // nothing to read: the kernel refused
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	exe, _ := os.Executable()
	b, _ := json.Marshal(probeIn{STI: "id\n"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := Command(ctx, Spec{Argv: []string{exe, probeArg, string(b)}, Binds: probeBinds(exe),
		Seccomp: ProfileVMM, Env: probeEnv})
	if err != nil {
		res.Probe = err.Error()
		return report(res)
	}
	cmd.Stdin = os.Stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		res.Probe = err.Error()
		return report(res)
	}
	var out []byte
	if err := cmd.Start(); err == nil {
		out, _ = io.ReadAll(stdout)
		_ = cmd.Wait()
	}
	var po probeOut
	_ = json.Unmarshal(out, &po)
	res.Probe = po.STI
	_ = unix.SetNonblock(0, true)
	got := make([]byte, 64)
	n, _ := unix.Read(0, got)
	res.Injected = string(got[:max(n, 0)])
	return report(res)
}

// TestNoTerminalInjection gives a jail the caller's terminal as stdin. The
// jail's new session, and the seccomp rule, keep it from pushing input
// into that terminal (CVE-2017-5226).
func TestNoTerminalInjection(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Skipf("no pseudo-terminal: %v", err)
			}
			defer func() { _ = ptmx.Close() }()
			arg, _ := json.Marshal(struct{ Userns bool }{m.userns})
			cmd := exec.CommandContext(t.Context(), testExe(t), ttyArg, string(arg))
			cmd.Env = probeEnv
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = tty.Close()
			defer func() { _ = cmd.Wait() }()
			got := make(chan ttyResult, 1)
			go func() {
				sc := bufio.NewScanner(ptmx)
				for sc.Scan() {
					if line, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "RESULT "); ok {
						var r ttyResult
						_ = json.Unmarshal([]byte(line), &r)
						got <- r
						return
					}
				}
			}()
			var res ttyResult
			select {
			case res = <-got:
			case <-time.After(60 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("the terminal host gave no result in 60 s")
			}
			t.Logf("%+v", res)
			if res.Control != "ok" {
				t.Skipf("this kernel refuses TIOCSTI on the caller's own terminal: %s", res.Control)
			}
			if res.Probe == "ok" || strings.Contains(res.Injected, "id") {
				t.Errorf("the jail pushed %q into the caller's terminal (TIOCSTI: %s)", res.Injected, res.Probe)
			}
		})
	}
}

// TestBindsArePinned checks that a bind names the object that Command
// pinned: a link to a path that no jail may show is refused, and a path
// that names another object at Start makes the jail fail.
func TestBindsArePinned(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			for target, want := range map[string]string{"/": "the root", home(t): "the home folder"} {
				alias := filepath.Join(dir, "alias"+strconv.Itoa(len(want)))
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
				_, err := Command(t.Context(), Spec{Argv: []string{"/bin/true"}, Binds: append(libBinds(), Bind{Path: alias}),
					Seccomp: ProfileConverter})
				if err == nil || !strings.Contains(err.Error(), want) || hintOf(err) == "" {
					t.Errorf("a bind of a link to %s = %v, want a refusal that names %s", target, err, want)
				}
			}
			// Swap the bound folder for a link to another one between Command and Start.
			bound, other := filepath.Join(dir, "bound"), filepath.Join(dir, "other")
			for _, d := range []string{bound, other} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(other+"/secret", []byte("SECRET"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/cat", bound + "/secret"},
				Binds: append(libBinds(), Bind{Path: bound}), Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(bound, bound+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, bound); err != nil {
				t.Fatal(err)
			}
			// Start refuses the moved object. TestReopenRefusesAMovedBind checks
			// the same refusal in the jail's init, for a swap after Start's check.
			out, err := cmd.CombinedOutput()
			if strings.Contains(string(out), "SECRET") || err == nil || !strings.Contains(err.Error(), "changed") || hintOf(err) == "" {
				t.Errorf("a swapped bind = %v, %q; want a refusal with the word changed, and a hint", err, out)
			}
		})
	}
}

// TestRulesStayBeneathTheirBinds checks that a rule's path through a link
// is refused, as Landlock would give its rights to the link's target.
func TestRulesStayBeneathTheirBinds(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir, secret := t.TempDir(), t.TempDir()
			if err := os.Symlink(secret, dir+"/l"); err != nil {
				t.Fatal(err)
			}
			_, err := Command(t.Context(), Spec{Argv: []string{"/bin/true"}, Binds: append(libBinds(), Bind{Path: dir}),
				Rules: []PathRule{{Path: dir + "/l", Access: Read | Write}}, Seccomp: ProfileConverter})
			if err == nil || hintOf(err) == "" {
				t.Errorf("a rule through a link out of its bind = %v, want a refusal with a hint", err)
			}
		})
	}
}

// TestNestedBindsWithOtherRightsAreRefused checks both modes refuse what
// the Landlock mode could not enforce.
func TestNestedBindsWithOtherRightsAreRefused(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			if err := os.Mkdir(dir+"/in", 0o755); err != nil {
				t.Fatal(err)
			}
			for _, binds := range [][]Bind{{{Path: dir, Write: true}, {Path: dir + "/in"}}, {{Path: dir}, {Path: dir + "/in", Write: true}}} {
				_, err := Command(t.Context(), Spec{Argv: []string{"/bin/true"}, Binds: append(libBinds(), binds...), Seccomp: ProfileConverter})
				if err == nil || !strings.Contains(err.Error(), "with other rights") {
					t.Errorf("binds %+v = %v, want a refusal", binds, err)
				}
			}
		})
	}
}

// outFile is a temporary file that holds a jail's output: a jail copies no
// stdio, so a test gives it a file, and reads the file.
type outFile struct{ f *os.File }

func newOutFile(t *testing.T) *outFile {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return &outFile{f}
}

// String returns what the file holds now.
func (o *outFile) String() string {
	b, _ := os.ReadFile(o.f.Name())
	return string(b)
}

// waitFor waits until buf holds want.
func waitFor(t *testing.T, buf fmt.Stringer, want string) {
	t.Helper()
	for range 200 {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no %q in %q", want, buf.String())
}

// TestCancelEndsTheWholeJail cancels a jail whose command started a child
// that holds stdout: Wait must return, so that child must be gone.
func TestCancelEndsTheWholeJail(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd, err := Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", "sleep 600 & echo started; wait"},
				Binds: libBinds(), Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			buf := newOutFile(t)
			cmd.Stdout, cmd.Stderr = buf.f, buf.f
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, buf, "started")
			cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Wait = %v, want the context's error", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("Wait did not return: a process of the jail still holds its stdout")
			}
		})
	}
}

// TestTheCommandEndsByItsOwnSignal sends SIGTERM to a shell that cleans
// up, resets its handler and raises the signal again, as git does. As PID
// 1 of a namespace it would survive; as init's child it ends.
func TestTheCommandEndsByItsOwnSignal(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c",
				`trap 'trap - TERM; kill -TERM $$; echo SURVIVED; exit 3' TERM; echo ready; while :; do sleep 0.05; done`},
				Binds: libBinds(), Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			buf := newOutFile(t)
			cmd.Stdout, cmd.Stderr = buf.f, buf.f
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, buf, "ready")
			if err := cmd.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var ee *ExitError
			if strings.Contains(buf.String(), "SURVIVED") || !errors.As(err, &ee) || ee.Signal != syscall.SIGTERM {
				t.Errorf("Wait = %v, output %q; want the command ended by SIGTERM", err, buf.String())
			}
		})
	}
}

// TestOrphansEndWithTheCommand checks, without namespaces, that a child
// that the command left behind ends when the command ends.
func TestOrphansEndWithTheCommand(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = read.Close(), write.Close() }()
	exe := testExe(t)
	script := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "' " + holdArg + " >/dev/null 2>&1 & read hold; exit 0"
	c, err := Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", script}, Binds: probeBinds(exe),
		Seccomp: ProfileConverter, Env: probeEnv, Files: []*os.File{write}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	waited := false
	defer func() {
		if !waited {
			c.killGroup()
			_ = c.Wait()
		}
	}()
	_ = read.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(read)
	if line, err := reader.ReadString('\n'); err != nil || line != "child-ready\n" {
		t.Fatalf("the orphan did not hold its inherited descriptor: %q, %v", line, err)
	}
	// The helper never closes fd3 voluntarily. Even with stdout/stderr closed,
	// its EOF proves the owned orphan ended, without signaling a recycled PID.
	_ = input.Close()
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("the orphan outlived its command before Wait: %v", err)
	}
	err = c.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
}

// TestLandlockModeKeepsItsSession checks that, without namespaces, a jail
// cannot start a session of its own, and so leave its process group.
func TestLandlockModeKeepsItsSession(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	if out := jailed(t, Spec{Seccomp: ProfileConverter}, probeIn{Setsid: true}); out.Setsid == "ok" {
		t.Error("a jail without namespaces started a new session")
	}
}
