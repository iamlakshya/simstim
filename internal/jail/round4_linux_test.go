package jail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openFDs returns each descriptor that this process holds, with what it
// names.
func openFDs(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		// The listing's own descriptor is closed by now, and is left out.
		if what, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil {
			out[e.Name()] = what
		}
	}
	return out
}

// newFDs returns the descriptors of now that before did not hold. A file
// that an earlier test left to the garbage collector may close meanwhile,
// so now may hold fewer.
func newFDs(before, now map[string]string) []string {
	var out []string
	for fd, what := range now {
		if before[fd] != what {
			out = append(out, fd+" "+what)
		}
	}
	return out
}

// readEnds says whether a read of r ends within d.
func readEnds(r io.Reader, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(r)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestAFailedStartClosesItsPipes makes all three pipes, then makes Start
// fail: on stdio that is not a file, on a context cancelled before Start,
// and on a cancel during the set-up. Start must close both ends of each
// pipe, so a read of the caller's end ends, and no descriptor is left. A
// jail that runs closes its own ends at Start, and Wait closes the rest.
func TestAFailedStartClosesItsPipes(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	if r, w, err := os.Pipe(); err == nil { // the runtime's poller, made once
		_, _ = r.Close(), w.Close()
	}
	for _, how := range []string{"stdio that is not a file", "cancelled before Start", "cancelled during the set-up", "a jail that ran"} {
		before := openFDs(t)
		ctx, cancel := context.WithCancel(t.Context())
		var cmd *Cmd
		if how == "cancelled during the set-up" {
			cmd = stalledJail(t, ctx)
		} else {
			var err error
			if cmd, err = Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", "cat; echo out; echo err >&2"},
				Binds: libBinds(), Seccomp: ProfileConverter}); err != nil {
				t.Fatal(err)
			}
		}
		stdin, err1 := cmd.StdinPipe()
		stdout, err2 := cmd.StdoutPipe()
		stderr, err3 := cmd.StderrPipe()
		if err := errors.Join(err1, err2, err3); err != nil {
			t.Fatal(err)
		}
		switch how {
		case "stdio that is not a file":
			cmd.Stdout = &bytes.Buffer{}
		case "cancelled before Start":
			cancel()
		case "cancelled during the set-up":
			time.AfterFunc(300*time.Millisecond, cancel)
		}
		err := cmd.Start()
		if how == "a jail that ran" {
			if err != nil {
				t.Fatal(err)
			}
			_ = stdin.Close()
			var out, errOut []byte
			read := make(chan struct{})
			go func() {
				out, _ = io.ReadAll(stdout)
				errOut, _ = io.ReadAll(stderr)
				close(read)
			}()
			select {
			case <-read:
			case <-time.After(15 * time.Second):
				cancel()
				t.Fatalf("%s: the reads of the pipes did not end: a copy of the jail's ends is still open here", how)
			}
			if err := cmd.Wait(); err != nil || string(out) != "out\n" || string(errOut) != "err\n" {
				t.Errorf("%s: Wait = %v, stdout %q, stderr %q", how, err, out, errOut)
			}
		} else {
			if err == nil {
				t.Fatalf("%s: Start worked", how)
			}
			for name, r := range map[string]io.Reader{"StdoutPipe": stdout, "StderrPipe": stderr} {
				if !readEnds(r, 3*time.Second) {
					t.Errorf("%s: a read of %s still blocks after Start failed", how, name)
				}
			}
			if _, err := stdin.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
				t.Errorf("%s: a write to StdinPipe = %v, want os.ErrClosed", how, err)
			}
		}
		left := newFDs(before, openFDs(t))
		for i := 0; i < 40 && len(left) > 0; i++ { // the set-up's writer closes its pipe last
			time.Sleep(50 * time.Millisecond)
			left = newFDs(before, openFDs(t))
		}
		if len(left) > 0 {
			t.Errorf("%s: descriptors left open: %v", how, left)
		}
		cancel()
	}
}

// TestRootfsIn reads mountinfo texts: a rootfs root, an overlay root as
// in a container, a root mounted over the rootfs, and a rootfs elsewhere.
// The test host's own root is never a rootfs.
func TestRootfsIn(t *testing.T) {
	const (
		rootfs  = "1 1 0:2 / / rw - rootfs rootfs rw,size=1000k\n20 1 0:20 / /proc rw,nosuid - proc proc rw\n"
		overlay = "512 470 0:52 / / rw,relatime master:221 - overlay overlay rw,lowerdir=/a,upperdir=/b,workdir=/c\n" +
			"600 512 0:55 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n"
		over      = rootfs + "21 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw\n"
		elsewhere = "21 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n30 21 0:30 / /mnt rw - rootfs rootfs rw\n"
	)
	for text, want := range map[string]bool{rootfs: true, overlay: false, over: false, elsewhere: false, "": false} {
		if got := rootfsIn(text); got != want {
			t.Errorf("rootfsIn(%q) = %v, want %v", text, got, want)
		}
	}
	if onRootfs() {
		t.Error("onRootfs says that the test host's root is a rootfs")
	}
}

// TestASwapBetweenCommandAndStartIsRefused moves a read-only bind's folder
// into a writable bind after Command, and puts a link at its old name. The
// object is the same, but its path is not: Start must refuse, and the jail
// must not write into the folder.
func TestASwapBetweenCommandAndStartIsRefused(t *testing.T) {
	for _, m := range bothModes {
		t.Run(m.name, func(t *testing.T) {
			inMode(t, m)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{"ro/a", "rw"} {
				if err := os.MkdirAll(dir+"/"+d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", "echo x > " + dir + "/rw/a/w"},
				Binds: append(libBinds(), Bind{Path: dir + "/ro/a"}, Bind{Path: dir + "/rw", Write: true}), Seccomp: ProfileConverter})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dir+"/ro/a", dir+"/rw/a"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+"/rw/a", dir+"/ro/a"); err != nil {
				t.Fatal(err)
			}
			err = cmd.Start()
			if err == nil {
				t.Errorf("Start after the swap worked: %v", cmd.Wait())
			} else if !strings.Contains(err.Error(), "moved") || hintOf(err) == "" {
				t.Errorf("Start after the swap = %v (hint %q), want a refusal that says it moved", err, hintOf(err))
			}
			if _, err := os.Stat(dir + "/rw/a/w"); err == nil {
				t.Error("the jail wrote into the folder that it was given read-only")
			}
		})
	}
}

// TestReopenRefusesAMovedBind runs the stage's own check, for a swap after
// Start's check: a bind whose object moved, with a link at its old name, is
// refused, and so is a new object at the bound path.
func TestReopenRefusesAMovedBind(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+"/a", 0o755); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(dir+"/a", &st); err != nil {
		t.Fatal(err)
	}
	b := bind{Path: dir + "/a", Kind: kindDir, Dev: st.Dev, Ino: st.Ino, Real: dir + "/a"}
	fds, err := reopen([]bind{b})
	if err != nil {
		t.Fatalf("reopen before the move: %v", err)
	}
	_ = unix.Close(fds[0])
	if err := os.Rename(dir+"/a", dir+"/b"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir+"/b", dir+"/a"); err != nil {
		t.Fatal(err)
	}
	if fds, err := reopen([]bind{b}); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Errorf("reopen after the move = %v, %v; want a refusal that says it moved", fds, err)
	}
	if err := os.Remove(dir + "/a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+"/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if fds, err := reopen([]bind{b}); err == nil || !strings.Contains(err.Error(), "another file") {
		t.Errorf("reopen of a new folder at the path = %v, %v; want a refusal that says it is another file", fds, err)
	}
}

// limitFile returns a file that holds a namespace limit.
func limitFile(t *testing.T, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "max_user_namespaces")
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestENOSPCIsARefusalOnlyWithNoUserNamespaces checks clone's ENOSPC: a
// clear no only when max_user_namespaces is 0. Below another limit, other
// processes hold the namespaces for now, so the next jail probes again.
// In the probe's build, ENOSPC is never a clear no.
func TestENOSPCIsARefusalOnlyWithNoUserNamespaces(t *testing.T) {
	cloneErr := &fs.PathError{Op: "fork/exec", Err: unix.ENOSPC}
	for _, c := range []struct {
		limit   string
		refusal bool
	}{{"0", true}, {"15000", false}, {"", false}} {
		withProbe(t, func() {
			maxUserNS = limitFile(t, c.limit)
			if c.limit == "" {
				maxUserNS = filepath.Join(t.TempDir(), "none")
			}
			ok, err := classifyProbe(cloneErr, false)
			switch {
			case c.refusal && (ok || err != nil):
				t.Errorf("limit %q: classifyProbe = %v, %v, want a clear no", c.limit, ok, err)
			case !c.refusal && (ok || err == nil || hintOf(err) == ""):
				t.Errorf("limit %q: classifyProbe = %v, %v, want an error with a hint", c.limit, ok, err)
			}
		})
	}
	if got := probeExit(unix.ENOSPC); got != probeUnclear {
		t.Errorf("probeExit(ENOSPC) = %d, want %d", got, probeUnclear)
	}
}
