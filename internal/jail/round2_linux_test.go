package jail

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// testHooks runs in a stage of a test's jail, before Init: it fakes a
// kernel without close_range ("range"), and then a /proc that cannot be
// read ("both") or a descriptor that cannot be marked ("setfd"). With
// stall, the stage never reads its set-up.
func testHooks(fdFail string, stall bool) {
	if stall {
		time.Sleep(time.Hour) // a stage that never reads its set-up
	}
	if fdFail == "" {
		return
	}
	closeRange = func(uint, uint, uint) error { return unix.ENOSYS }
	switch fdFail {
	case "both":
		fdDir = "/nonexistent-fd-folder"
	case "setfd":
		setCloexec = func(int) error { return unix.EIO }
	}
}

// TestNoMetadataChangesOutsideTheBinds changes the mode, owner, times and
// an xattr of a file outside every bind, which Landlock does not check.
// Unjailed, each call works on a control file. Jailed, each fails, and the
// file stays as it was.
func TestNoMetadataChangesOutsideTheBinds(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			target, control := dir+"/target", dir+"/control"
			for _, f := range []string{target, control} {
				if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			old := time.Unix(1_000_000_000, 0)
			if err := os.Chtimes(target, old, old); err != nil {
				t.Fatal(err)
			}
			c := unjailed(t, probeIn{Meta: control})
			free, _, _ := start(t, c, probeIn{Meta: control})
			_ = c.Wait()
			out := jailed(t, Spec{Seccomp: ProfileConverter}, probeIn{Meta: target})
			for call, got := range out.Meta {
				if free.Meta[call] != "ok" {
					t.Logf("%s does not work here even unjailed: %s", call, free.Meta[call])
					continue
				}
				if got == "ok" {
					t.Errorf("the jail could %s a file outside its binds", call)
				}
			}
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 || !fi.ModTime().Equal(old) {
				t.Errorf("the file changed: mode %v, time %v", fi.Mode(), fi.ModTime())
			}
			if n, _ := unix.Getxattr(target, "user.jail", nil); n > 0 {
				t.Error("the file got an xattr")
			}
		})
	}
}

// TestAliasesOfBindsAreRefused binds a writable folder, then a link to its
// child read-only: Landlock would give the folder's rights through the
// link, so both modes refuse it. A second link to the folder itself names
// the same file, and is refused too.
func TestAliasesOfBindsAreRefused(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir, links := t.TempDir(), t.TempDir()
			if err := os.Mkdir(dir+"/child", 0o755); err != nil {
				t.Fatal(err)
			}
			for name, target := range map[string]string{"child": dir + "/child", "same": dir} {
				if err := os.Symlink(target, links+"/"+name); err != nil {
					t.Fatal(err)
				}
			}
			for alias, want := range map[Bind]string{
				{Path: links + "/child"}:             "with other rights",
				{Path: links + "/same", Write: true}: "name the same file",
			} {
				_, err := Command(t.Context(), Spec{Argv: []string{"/bin/true"},
					Binds: append(libBinds(), Bind{Path: dir, Write: true}, alias), Seccomp: ProfileConverter})
				if err == nil || !strings.Contains(err.Error(), want) || hintOf(err) == "" {
					t.Errorf("bind %+v beside the writable %s = %v, want a refusal: %s", alias, dir, err, want)
				}
			}
		})
	}
}

// TestAnExecRuleOpensOnlyItsPath gives Exec to one folder of a writable
// bind. With namespaces, that folder runs files, and the loader still
// cannot run a file from the rest of the bind. Without them, Landlock
// refuses execve on the rest, but the loader runs it (spec 5.6).
func TestAnExecRuleOpensOnlyItsPath(t *testing.T) {
	ld := "/lib/ld-linux-aarch64.so.1"
	if runtime.GOARCH == "amd64" {
		ld = "/lib64/ld-linux-x86-64.so.2"
	}
	b, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			for _, d := range []string{"allowed", "denied"} {
				if err := os.Mkdir(dir+"/"+d, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir+"/"+d+"/t", b, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c",
				dir + "/allowed/t && echo ALLOWED; " + dir + "/denied/t && echo DIRECT; " + ld + " " + dir + "/denied/t && echo LOADER"},
				Binds: append(libBinds(), Bind{Path: dir, Write: true}),
				Rules: []PathRule{{Path: dir + "/allowed", Access: Exec}}, Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			out, _ := cmd.CombinedOutput()
			t.Logf("%q", out)
			if !strings.Contains(string(out), "ALLOWED") || strings.Contains(string(out), "DIRECT") {
				t.Errorf("the Exec rule's folder should run files, and no other folder: %q", out)
			}
			if m.userns && strings.Contains(string(out), "LOADER") {
				t.Errorf("the loader ran a file from beside the Exec rule's folder: %q", out)
			}
		})
	}
}

// TestDescriptorCleanupFailsClosed fakes a kernel without close_range: the
// jail marks each descriptor through /proc/self/fd. When that cannot be
// read, or a descriptor cannot be marked, the jail stops with 125.
func TestDescriptorCleanupFailsClosed(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			for fail, want := range map[string]int{"range": 0, "both": setupFailed, "setfd": setupFailed} {
				cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/true"}, Binds: libBinds(),
					Env: []string{"JAILTEST_FDFAIL=" + fail}, Seccomp: ProfileConverter})
				if err != nil {
					t.Fatal(err)
				}
				out, _ := cmd.CombinedOutput()
				if cmd.ExitCode() != want || (want != 0 && !strings.Contains(string(out), "close-on-exec")) {
					t.Errorf("%s: exit %d, want %d\n%s", fail, cmd.ExitCode(), want, out)
				}
			}
		})
	}
}

// TestProbeClassification feeds the probe's real ends to classifyProbe:
// only the probe's success and the kernel's clear refusals are answers.
func TestProbeClassification(t *testing.T) {
	old := maxUserNS
	t.Cleanup(func() { maxUserNS = old })
	maxUserNS = limitFile(t, "15000")
	end := func(script string) error {
		return exec.CommandContext(t.Context(), "/bin/sh", "-c", script).Run()
	}
	const (
		yes   = "yes"
		no    = "no"
		retry = "retry"
	)
	cases := []struct {
		name     string
		err      error
		timedOut bool
		want     string
	}{
		{"built", nil, false, yes},
		{"the probe's refusal", end("exit 1"), false, no},
		{"clone refused", &fs.PathError{Op: "fork/exec", Err: unix.EPERM}, false, no},
		{"no namespace left for now", &fs.PathError{Op: "fork/exec", Err: unix.ENOSPC}, false, retry},
		{"a uid_map write refused", &os.SyscallError{Syscall: "write", Err: unix.EACCES}, false, no},
		{"timed out", end("exit 1"), true, retry},
		{"short of memory in the probe", end("exit 2"), false, retry},
		{"an unexpected exit", end("exit 7"), false, retry},
		{"killed", end("kill -9 $$"), false, retry},
		{"EMFILE", &fs.PathError{Op: "fork/exec", Err: unix.EMFILE}, false, retry},
		{"ENFILE", &fs.PathError{Op: "fork/exec", Err: unix.ENFILE}, false, retry},
		{"EAGAIN", &fs.PathError{Op: "fork/exec", Err: unix.EAGAIN}, false, retry},
		{"ENOMEM", &fs.PathError{Op: "fork/exec", Err: unix.ENOMEM}, false, retry},
	}
	for _, c := range cases {
		ok, err := classifyProbe(c.err, c.timedOut)
		got := no
		switch {
		case err != nil && hintOf(err) != "":
			got = retry
		case err != nil:
			got = "an error without a hint"
		case ok:
			got = yes
		}
		if got != c.want {
			t.Errorf("%s: classifyProbe = %v, %v: %s, want %s", c.name, ok, err, got, c.want)
		}
	}
	for _, c := range []struct {
		err  error
		want int
	}{{nil, 0}, {unix.EPERM, probeRefused}, {unix.EINVAL, probeUnclear}, {unix.ENOMEM, probeUnclear},
		{unix.EMFILE, probeUnclear}, {errors.New("odd"), probeUnclear}} {
		if got := probeExit(c.err); got != c.want {
			t.Errorf("probeExit(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// TestNoOpenat2 fakes a kernel before Linux 5.6: Command refuses, and names
// the version.
func TestNoOpenat2(t *testing.T) {
	old := openat2
	t.Cleanup(func() { openat2 = old })
	openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
	exe := testExe(t)
	_, err := Command(t.Context(), Spec{Argv: []string{exe}, Binds: []Bind{{Path: exe}}, Seccomp: ProfileConverter})
	if err == nil || !strings.Contains(err.Error(), "openat2") || !strings.Contains(hintOf(err), "Linux 5.6") {
		t.Errorf("Command without openat2 = %v (hint %q), want a refusal that names Linux 5.6", err, hintOf(err))
	}
}

// stalledJail returns a jail whose first stage never reads its set-up,
// which is larger than a pipe holds.
func stalledJail(t *testing.T, ctx context.Context) *Cmd {
	t.Helper()
	args := []string{"/bin/true"}
	for range 300 {
		args = append(args, strings.Repeat("x", 1024))
	}
	cmd, err := Command(ctx, Spec{Argv: args, Binds: libBinds(), Env: []string{"JAILTEST_STALL=1"}, Seccomp: ProfileConverter})
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// TestStartStopsDuringTheSetup cancels the context, and in a second jail
// calls Signal, while Start writes a set-up that the stage does not read:
// Start must return an error, with the stage killed and reaped.
func TestStartStopsDuringTheSetup(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	for _, how := range []string{"cancel", "signal"} {
		ctx, cancel := context.WithCancel(t.Context())
		cmd := stalledJail(t, ctx)
		done := make(chan error, 1)
		go func() { done <- cmd.Start() }()
		time.Sleep(300 * time.Millisecond)
		if how == "cancel" {
			cancel()
		} else if err := cmd.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("Signal during the set-up = %v", err)
		}
		select {
		case err := <-done:
			if err == nil || (how == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Errorf("%s: Start = %v, want an error", how, err)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("%s: Start did not return while the stage did not read its set-up", how)
		}
		if err := unix.Kill(cmd.Pid(), 0); !errors.Is(err, unix.ESRCH) {
			t.Errorf("%s: the stage %d still exists: %v", how, cmd.Pid(), err)
		}
		cancel()
	}
}

// TestCmdStatesNeverPanic calls the wrapper's methods out of order.
func TestCmdStatesNeverPanic(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	spec := Spec{Argv: []string{"/bin/true"}, Binds: libBinds(), Seccomp: ProfileConverter}
	cmd, err := Command(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"Signal before Start": cmd.Signal(syscall.SIGTERM), "Wait before Start": cmd.Wait()} {
		if err == nil || hintOf(err) == "" {
			t.Errorf("%s = %v, want an error with a hint", name, err)
		}
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err == nil || hintOf(err) == "" {
		t.Errorf("Start twice = %v, want an error with a hint", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"Wait twice": cmd.Wait(), "Signal after Wait": cmd.Signal(syscall.SIGTERM)} {
		if err == nil || hintOf(err) == "" {
			t.Errorf("%s = %v, want an error with a hint", name, err)
		}
	}
	// A Start that failed: the context ended first.
	ctx, cancel := context.WithCancel(t.Context())
	failed, err := Command(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := failed.Start(); err == nil {
		t.Error("Start after the context ended worked")
	}
	for name, err := range map[string]error{"Signal": failed.Signal(syscall.SIGTERM), "Wait": failed.Wait()} {
		if err == nil || hintOf(err) == "" {
			t.Errorf("%s after a failed Start = %v, want an error with a hint", name, err)
		}
	}
}

// TestRenamesAndLinksInAWritableBind moves and links a file between two
// folders of one writable bind, which needs Landlock's refer right.
func TestRenamesAndLinksInAWritableBind(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			for _, d := range []string{"a", "b"} {
				if err := os.Mkdir(dir+"/"+d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(dir+"/a/f", []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := jailed(t, Spec{Binds: []Bind{{Path: dir, Write: true}}, Seccomp: ProfileConverter},
				probeIn{Moves: [][2]string{{dir + "/a/f", dir + "/b/f"}}, Links: [][2]string{{dir + "/b/f", dir + "/a/g"}}})
			if len(out.Moves) != 1 || out.Moves[0] != "ok" || len(out.Links) != 1 || out.Links[0] != "ok" {
				t.Errorf("rename %v and link %v in a writable bind, want both ok", out.Moves, out.Links)
			}
		})
	}
}

// TestAConverterWithNoBindsRuns builds this package's tests as a static
// binary, so that the jail's stages need no C library, and runs, in it, a
// converter jail with no binds at all.
func TestAConverterWithNoBindsRuns(t *testing.T) {
	if os.Getenv("JAILTEST_INNER") == "1" {
		t.Skip("this is the inner run")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go tool to build a static test binary: %v", err)
	}
	static := filepath.Join(t.TempDir(), "static.test")
	build := exec.CommandContext(t.Context(), goTool, "test", "-c", "-buildvcs=false", "-o", static, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build a static test binary: %v\n%s", err, out)
	}
	run := exec.CommandContext(t.Context(), static, "-test.run", "^TestZeroBindsInside$", "-test.v")
	run.Env = append(os.Environ(), "JAILTEST_INNER=1")
	out, err := run.CombinedOutput()
	t.Logf("%s", out)
	if err != nil || !strings.Contains(string(out), "--- PASS: TestZeroBindsInside") {
		t.Errorf("the converter with no binds: %v", err)
	}
}

// TestZeroBindsInside runs only in TestAConverterWithNoBindsRuns's static
// binary.
func TestZeroBindsInside(t *testing.T) {
	if os.Getenv("JAILTEST_INNER") != "1" {
		t.Skip("runs inside TestAConverterWithNoBindsRuns")
	}
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			f, err := os.Open(testExe(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			in := probeIn{}
			out := jailed(t, Spec{Argv: probeArgs(t, "/proc/self/fd/3", in), Files: []*os.File{f}, Seccomp: ProfileConverter}, in)
			if out.Args != 3 {
				t.Errorf("the probe saw %d arguments", out.Args)
			}
		})
	}
}
