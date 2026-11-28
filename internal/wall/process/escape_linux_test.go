//go:build linux

package process

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestSeccompBPFAssembles(t *testing.T) {
	raw, err := SeccompBPF()
	if err != nil {
		t.Fatalf("SeccompBPF: %v", err)
	}
	if len(raw) == 0 || len(raw)%8 != 0 {
		t.Fatalf("SeccompBPF len = %d, want non-zero multiple of 8", len(raw))
	}
	// First instruction must load seccomp_data.arch (offset 4) for the host-ABI guard.
	firstOp := binary.NativeEndian.Uint16(raw[0:2])
	firstK := binary.NativeEndian.Uint32(raw[4:8])
	if firstOp != 0x20 || firstK != 4 {
		t.Fatalf("first BPF inst op=0x%x k=%d, want 0x20/4 (LD arch)", firstOp, firstK)
	}
}

func TestDriverPrepareAndOpen(t *testing.T) {
	tmp := t.TempDir()
	sessDir := filepath.Join(tmp, "sess")
	ws := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	drv := Driver{}
	ctx := context.Background()
	sb, err := drv.Prepare(ctx, wall.Spec{
		ID:         "s1234567",
		SessionDir: sessDir,
		Helper:     "/usr/local/bin/" + brand.HostBinary,
		Mounts:     []wall.Mount{{Host: ws, Guest: "/work/repo", Write: true}},
		StateDirs:  []string{".claude/projects"},
	})
	if err != nil {
		if strings.Contains(err.Error(), "bubblewrap (bwrap) is not installed") {
			t.Skip("bwrap not installed")
		}
		t.Fatalf("Prepare: %v", err)
	}
	if sb == nil {
		t.Fatal("Prepare returned nil sandbox")
	}
	opened, err := drv.Open(ctx, sessDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened == nil {
		t.Fatal("Open returned nil sandbox")
	}
}

func TestProcessWallEscapeSuite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable on this host: %s (%s)", probe.Reason, probe.Fix)
	}

	testBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	escapePy, err := filepath.Abs(filepath.Join("..", "..", "..", "test", "e2e", "escape.py"))
	if err != nil {
		t.Fatalf("abs escape.py: %v", err)
	}
	if _, err := os.Stat(escapePy); err != nil {
		t.Fatalf("escape.py missing at %s: %v", escapePy, err)
	}

	tmp := t.TempDir()
	ws := filepath.Join(tmp, "repo")
	stateDir := filepath.Join(tmp, "state")
	canary := filepath.Join(tmp, "host-secret-canary.txt")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canary, []byte("HOST_SECRET_CANARY"), 0o600); err != nil {
		t.Fatal(err)
	}

	hostHome, _ := os.UserHomeDir()
	spec := Spec{
		SessionID:      "esctest1",
		HelperExe:      testBin,
		WorkspaceHost:  ws,
		WorkspaceGuest: "/work/repo",
		StateHost:      stateDir,
		StateDirs:      []string{".claude/projects"},
		ExtraRO:        []Mount{{Source: escapePy, Dest: "/opt/agents/escape.py", ReadOnly: true}},
		ExtraEnv: []string{
			"PROCESS_WALL_ESCAPE_HELPER=1",
			"ESCAPE_HOST_HOME=" + hostHome,
			"ESCAPE_HOST_CANARY=" + canary,
			"ESCAPE_HOST_PID=" + strconv.Itoa(os.Getpid()),
			"ESCAPE_HOST_RUNTIME=" + os.Getenv("XDG_RUNTIME_DIR"),
			"ESCAPE_WORKSPACE=/work/repo",
		},
		DisableUserNS: supportsDisableUserNS(ctx, ""),
	}
	plan, err := BuildPlan(spec)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	// Replace the trailing "-- <guestHelper> wall-init" with the Go test helper.
	idx := len(plan.Args) - 3
	if idx < 0 || plan.Args[idx] != "--" {
		t.Fatalf("unexpected bwrap tail: %v", plan.Args)
	}
	args := append([]string(nil), plan.Args[:idx+1]...)
	args = append(args, plan.GuestHelper, "-test.run=^TestProcessWallInnerHelper$", "-test.v")

	secBytes, err := SeccompBPF()
	if err != nil {
		t.Fatalf("SeccompBPF: %v", err)
	}
	// fd 3 placeholder (socketpair slot), fd 4 seccomp, fd 5..8 synthetic /etc files.
	dummy, err := memFile("dummy-link", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dummy.Close()
	secFile, err := memFile("seccomp", secBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer secFile.Close()
	extraFiles := []*os.File{dummy, secFile}
	for _, bd := range plan.BindData {
		f, ferr := memFile(bd.Name, bd.Data)
		if ferr != nil {
			t.Fatal(ferr)
		}
		defer f.Close()
		extraFiles = append(extraFiles, f)
	}

	cmd := exec.CommandContext(ctx, plan.BwrapPath, args...)
	cmd.ExtraFiles = extraFiles
	out, err := cmd.CombinedOutput()
	t.Logf("inner escape probe output:\n%s", string(out))
	if err != nil {
		t.Fatalf("process-wall escape suite failed: %v", err)
	}
	if !strings.Contains(string(out), "escape suite: all checks passed") {
		t.Fatalf("missing escape.py success marker in output")
	}
}

// TestProcessWallInnerHelper runs ONLY inside TestProcessWallEscapeSuite's
// bubblewrap sandbox. It applies Landlock as wall-init does, runs Go-level
// escape checks (including PTY allocation, cross-dir rename, foreign ABIs and
// NETLINK_ROUTE), and then executes test/e2e/escape.py inside the same wall.
func TestProcessWallInnerHelper(t *testing.T) {
	if os.Getenv("PROCESS_WALL_ESCAPE_HELPER") != "1" {
		t.Skip("helper runs inside TestProcessWallEscapeSuite")
	}

	cfg, ok, err := LandlockFromEnv()
	if !ok || err != nil {
		t.Fatalf("LandlockFromEnv ok=%v err=%v", ok, err)
	}
	_ = os.Unsetenv(LandlockEnvVar())
	if err := ApplyLandlock(cfg); err != nil {
		t.Fatalf("ApplyLandlock: %v", err)
	}

	// 1. Cross-directory rename (requires Landlock ABI >= 2 WithRefer) and writes in workspace.
	d1 := "/work/repo/sub1"
	d2 := "/work/repo/sub2"
	if err := os.MkdirAll(d1, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", d1, err)
	}
	if err := os.MkdirAll(d2, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", d2, err)
	}
	src := filepath.Join(d1, "moved.txt")
	dst := filepath.Join(d2, "moved.txt")
	if err := os.WriteFile(src, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatalf("cross-dir rename inside workspace failed: %v", err)
	}

	// 2. PTY allocation must work under Landlock V5/V6 (WithIoctlDev on /dev/ptmx + /dev/pts).
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open inside Landlock+seccomp failed: %v", err)
	}
	_ = tty.Close()
	_ = ptmx.Close()

	// 3. Landlock denies writes outside workspace, /home/agent, /tmp, /var/tmp, /run/<brand>.
	for _, p := range []string{"/ll-denied", "/etc/ll-denied", "/usr/ll-denied", "/work/ll-denied", filepath.Join("/home", "ll-denied"), "/run/ll-denied"} {
		if err := os.WriteFile(p, []byte("bad"), 0o600); err == nil {
			_ = os.Remove(p)
			t.Fatalf("write to %s unexpectedly succeeded under Landlock", p)
		}
	}

	// 4. Go net.Interfaces() must succeed via read-only NETLINK_ROUTE and show only lo.
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces failed: %v", err)
	}
	for _, ifc := range ifaces {
		if ifc.Name != "lo" {
			t.Fatalf("unexpected non-lo interface %q in empty netns", ifc.Name)
		}
	}

	// 5. Foreign ABIs on amd64: i386 int 0x80 kills child with SIGSYS; x32 returns ENOSYS.
	if runtime.GOARCH == "amd64" && supportsInt80Check() {
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		child := exec.Command(self, "-test.run=^TestProcessWallInt80Child$")
		child.Env = append(os.Environ(), "PROCESS_WALL_INT80_CHILD=1")
		err = child.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("int80 child error = %v, want ExitError(SIGSYS)", err)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGSYS {
			t.Fatalf("int80 child wait status = %v (signal=%v), want SIGSYS", ws, ws.Signal())
		}

		// x32 syscall bit (0x40000000 | SYS_GETPID) must fail with ENOSYS.
		_, _, errno := unix.RawSyscall(0x40000000|unix.SYS_GETPID, 0, 0, 0)
		if errno != unix.ENOSYS {
			t.Fatalf("x32 getpid errno = %v, want ENOSYS", errno)
		}
	}

	// 6. Direct clone(CLONE_NEWUSER) / unshare / io_uring_setup / clone3 checks via unix.
	if _, _, e := unix.Syscall(unix.SYS_UNSHARE, unix.CLONE_NEWUSER, 0, 0); e != unix.EPERM {
		t.Fatalf("unshare(CLONE_NEWUSER) errno = %v, want EPERM", e)
	}
	if _, _, e := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0); e != unix.ENOSYS {
		t.Fatalf("clone3 errno = %v, want ENOSYS", e)
	}
	var zeroBuf [8]byte
	if _, _, e := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&zeroBuf[0])), 0); e != unix.EPERM {
		t.Fatalf("io_uring_setup errno = %v, want EPERM", e)
	}

	// 7. Run the standalone test/e2e/escape.py inside the exact same confined environment.
	py := exec.Command("python3", "/opt/agents/escape.py")
	pyOut, err := py.CombinedOutput()
	t.Logf("escape.py output:\n%s", string(pyOut))
	if err != nil {
		t.Fatalf("escape.py failed: %v", err)
	}
}

func TestProcessWallInt80Child(t *testing.T) {
	if os.Getenv("PROCESS_WALL_INT80_CHILD") != "1" {
		t.Skip("child runs inside TestProcessWallInnerHelper")
	}
	int80Unshare()
	t.Fatal("int80Unshare returned instead of being killed by seccomp")
}
