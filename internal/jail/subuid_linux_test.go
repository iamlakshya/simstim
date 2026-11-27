package jail

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// subuidOrSkip returns the user's subuid, or skips the test.
func subuidOrSkip(t *testing.T) subID {
	t.Helper()
	inMode(t, testMode{"namespaces", true})
	sub, ok := lookupSubID()
	if !ok {
		t.Skip("this user has no range in /etc/subuid and /etc/subgid, or no newuidmap and newgidmap " +
			"(apt install uidmap; usermod --add-subuids 100000-165535 --add-subgids 100000-165535 <user>)")
	}
	return sub
}

// TestSubuid runs a jail as the user's first subuid, under umask 077. A
// writable bind that the login user owns goes to the subuid, never open to
// every user. The four limits hold, RemoveAll removes what the subuid made,
// and the jail dies with its outer stage.
func TestSubuid(t *testing.T) {
	sub := subuidOrSkip(t)
	dir := filepath.Join(t.TempDir(), "open")
	exe := copyExe(t, dir)
	made := filepath.Join(dir, "made")
	openTo(t, made)
	t.Cleanup(func() { _ = RemoveAll(context.Background(), made) })
	binds := append(probeBinds(exe), Bind{Path: made, Write: true})
	in := probeIn{Home: home(t), Addr: "127.0.0.1:9", Pid: startTarget(t), WriteTo: made, Wait: true}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var cmd *Cmd
	var out probeOut
	var stderr *outFile
	var stdin io.WriteCloser
	inUmask(0o077, func() {
		cmd, err = Command(ctx, Spec{Argv: probeArgs(t, exe, in), Binds: binds, Subuid: true, Seccomp: ProfileVMM,
			Env: probeEnv, Files: []*os.File{w}})
		if err != nil {
			t.Fatal(err)
		}
		out, stdin, stderr = start(t, cmd, in)
	})
	defer func() { _ = stdin.Close() }()
	_ = w.Close()
	for what, got := range map[string]string{"open $HOME": out.Home, "connect over TCP": out.TCP,
		"trace another process": out.Ptrace, "open another process's memory": out.Mem} {
		if got == "ok" {
			t.Errorf("the jailed command could %s", what)
		}
	}
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(made, "made"), &st); err != nil {
		t.Fatalf("the probe made no file: %v\n%s", err, stderr)
	}
	if int(st.Uid) != sub.SubUID || int(st.Gid) != sub.SubGID {
		t.Errorf("the file belongs to %d:%d, want the subuid %d:%d", st.Uid, st.Gid, sub.SubUID, sub.SubGID)
	}
	if err := syscall.Stat(made, &st); err != nil || st.Mode&0o777 != 0o755 {
		t.Errorf("the writable bind's mode is %o, want 0755 and no more", st.Mode&0o777)
	}
	// Killing the jail kills its init too: the probe's end of the pipe closes.
	cancel()
	_ = cmd.Wait()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("the jail outlived its cancel")
	}
	if err := RemoveAll(t.Context(), made); err != nil {
		t.Errorf("RemoveAll of the subuid's folder: %v", err)
	}
	if _, err := os.Stat(made); !os.IsNotExist(err) {
		t.Errorf("the subuid's folder is still there: %v", err)
	}
	t.Logf("subuid %d ran the probe as uid %d inside", sub.SubUID, out.UID)
}

// TestSubuidUnderGCStress runs subuid jails while the outer stage collects
// garbage without a pause: its end of the pipe that tells init it still
// runs must stay open.
func TestSubuidUnderGCStress(t *testing.T) {
	subuidOrSkip(t)
	exe := copyExe(t, filepath.Join(t.TempDir(), "open"))
	for range 5 {
		in := probeIn{}
		s := Spec{Argv: probeArgs(t, exe, in), Binds: probeBinds(exe), Subuid: true, Seccomp: ProfileConverter,
			Env: append([]string{"JAILTEST_GC=1"}, probeEnv...)}
		cmd, err := Command(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("a subuid jail under GC: %v\n%s", err, out)
		}
	}
}

// TestWritableBindsRunNothingWithoutARule checks that a jail cannot run a
// file from a writable bind, unless a PathRule gives Exec. With namespaces
// the bind is noexec, so the loader cannot run it either. Without them,
// Landlock refuses execve only, and the loader still can.
func TestWritableBindsRunNothingWithoutARule(t *testing.T) {
	if kernelABI() == 0 {
		t.Skip("this kernel has no Landlock")
	}
	ld := "/lib/ld-linux-aarch64.so.1"
	if runtime.GOARCH == "amd64" {
		ld = "/lib64/ld-linux-x86-64.so.2"
	}
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			b, err := os.ReadFile("/bin/true")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir+"/t", b, 0o700); err != nil {
				t.Fatal(err)
			}
			rw := Bind{Path: dir, Write: true}
			out, _ := shJail(t, dir+"/t && echo DIRECT; "+ld+" "+dir+"/t && echo LOADER", rw)
			if strings.Contains(out, "DIRECT") {
				t.Errorf("a file from a writable bind ran: %q", out)
			}
			if m.userns && strings.Contains(out, "LOADER") {
				t.Errorf("the loader ran a file from a writable bind: %q", out)
			}
			t.Logf("without an Exec rule: %q", out)
			cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", dir + "/t && echo DIRECT"},
				Binds: append(libBinds(), rw), Rules: []PathRule{{Path: dir, Access: Exec}}, Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			if out, err := cmd.CombinedOutput(); !strings.Contains(string(out), "DIRECT") {
				t.Errorf("with an Exec rule, the file did not run: %v\n%s", err, out)
			}
		})
	}
}

// TestSection20Rows runs the two jails of spec 20 that have no bind for
// their program: a converter, with no binds but the C library, and a
// command that runs from one of its inherited files. The released helper is
// static, but this test binary, whose stages build the jail, loads the C
// library: a converter of the release needs no bind at all, as
// TestCheckSortsBindsAndAcceptsWhatSection20Needs shows.
func TestSection20Rows(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			helper := filepath.Join("..", "client", "helpers", "host-linux-"+runtime.GOARCH)
			if f, err := os.Open(helper); err == nil {
				defer func() { _ = f.Close() }()
				cmd, err := Command(t.Context(), Spec{Argv: []string{"/proc/self/fd/3", "version"}, Files: []*os.File{f},
					Binds: libBinds(), Seccomp: ProfileConverter})
				if err != nil {
					t.Fatal(err)
				}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("a converter with no binds: %v\n%s", err, out)
				}
			} else {
				t.Logf("no static helper to run without binds (run just helpers): %v", err)
			}
			f, err := os.Open(testExe(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			in := probeIn{}
			args := probeArgs(t, "/proc/self/fd/3", in)
			out := jailed(t, Spec{Argv: args, Binds: libBinds(), Files: []*os.File{f}, Seccomp: ProfileVMM}, in)
			if out.Args != 3 {
				t.Errorf("the probe from fd 3 saw %d arguments", out.Args)
			}
		})
	}
}

// TestLargeSetup passes a set-up of more than 128 KiB, which one
// environment string cannot hold.
func TestLargeSetup(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			exe := testExe(t)
			in := probeIn{}
			args := probeArgs(t, exe, in)
			for range 200 {
				args = append(args, strings.Repeat("x", 1024))
			}
			out := jailed(t, Spec{Argv: args, Binds: probeBinds(exe), Seccomp: ProfileConverter}, in)
			if out.Args != len(args) {
				t.Errorf("the probe saw %d arguments, want %d", out.Args, len(args))
			}
		})
	}
}

// TestGitAndShRun runs sh and git in a jail. Without namespaces they read
// only their own /proc/self and a short list of files of /proc, and no
// other process's.
func TestGitAndShRun(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir := t.TempDir()
			script := "echo sh-ok && cd " + dir + " && " + git + " init -q repo && cd repo && " +
				git + " -c user.name=j -c user.email=j@j commit -q --allow-empty -m x && " +
				git + " bundle create -q ../b.bundle HEAD && echo git-ok"
			s := Spec{Argv: []string{"/bin/sh", "-c", script},
				Env:   []string{"HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "PATH=/usr/bin:/bin"},
				Binds: append(libBinds(), Bind{Path: dir, Write: true}), Seccomp: ProfileGit}
			cmd, err := Command(t.Context(), s)
			if !m.userns {
				// git changes modes by path, as chmod of .git/config.lock, which
				// a jail without namespaces refuses: the git jail needs them.
				if err == nil || !strings.Contains(err.Error(), "git jail") || hintOf(err) == "" {
					t.Errorf("a git jail without namespaces = %v, want a refusal with a hint", err)
				}
				s.Argv, s.Seccomp = []string{"/bin/sh", "-c", "echo sh-ok"}, ProfileConverter
				if cmd, err = Command(t.Context(), s); err != nil {
					t.Fatal(err)
				}
				if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "sh-ok") {
					t.Errorf("sh in a jail: %v\n%s", err, out)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "git-ok") {
					t.Errorf("sh and git in a jail: %v\n%s", err, out)
				}
			}
			other := "/proc/" + strconv.Itoa(startTarget(t)) + "/cmdline"
			if out := jailed(t, Spec{Seccomp: ProfileConverter}, probeIn{Read: []string{other, "/proc/self/maps"}}); out.Read[other] == "ok" ||
				out.Read["/proc/self/maps"] != "ok" {
				t.Errorf("reads in /proc: %v, want only the command's own", out.Read)
			}
		})
	}
}
