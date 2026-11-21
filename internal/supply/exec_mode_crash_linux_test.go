//go:build linux

package supply

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func reopenModeFixtureV1(t *testing.T, dir string) *nativeModeFixtureV1 {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cachefs.OpenPrivateChild(r, "sha256", false)
	if err != nil {
		t.Fatal(err)
	}
	m, err := cachefs.OpenPrivateChild(r, "execmode", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "fixture-item.json"))
	if err != nil {
		t.Fatal(err)
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	file, err := cachefs.OpenFile(a, item.SHA256, os.O_RDONLY, 0o555)
	if err != nil {
		t.Fatal(err)
	}
	f := &nativeModeFixtureV1{root: r, artifacts: a, modes: m, file: file, item: item, dir: dir}
	t.Cleanup(f.Close)
	return f
}

func TestExecModeNativeSubprocessV1(t *testing.T) {
	role := os.Getenv("MODE_NATIVE_TEST_ROLE")
	if role == "" {
		return
	}
	if os.Getenv("FSVERITY_NATIVE_FIXTURE") == "" {
		t.Fatal("native subprocess has no admitted fixture")
	}
	if role == "holder" {
		lease := os.NewFile(3, "inherited-original-mode-OFD")
		defer lease.Close()
		pipe := os.NewFile(4, "fixture-lifetime-reader")
		defer pipe.Close()
		if _, err := io.Copy(io.Discard, pipe); err != nil {
			t.Fatal(err)
		}
		return
	}
	if role != "owner" {
		t.Fatal("unknown closed native fixture role")
	}
	f := reopenModeFixtureV1(t, os.Getenv("MODE_NATIVE_TEST_DIR"))
	owner := f.owner(t)
	lease, err := owner.BeginExecMode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	loan, err := lease.RetainForJailV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ArmV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestExecModeNativeSubprocessV1$")
	child.Env = append(os.Environ(), "MODE_NATIVE_TEST_ROLE=holder")
	child.ExtraFiles = []*os.File{loan, reader}
	created := -1
	child.SysProcAttr = &syscall.SysProcAttr{PidFD: &created}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if created < 0 {
		child.Process.Kill()
		child.Wait()
		t.Fatal("fixture child has no actual creation pidfd")
	}
	process := os.NewFile(uintptr(created), "actual-fixture-child-creation")
	controlFile := os.NewFile(3, "original-fixture-controller")
	connection, err := net.FileConn(controlFile)
	controlFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.SendUnixControlV1(connection.(*net.UnixConn), []byte{'h'}, []*os.File{process, writer}); err != nil {
		child.Process.Kill()
		child.Wait()
		t.Fatal(err)
	}
	// Deliberate real parent death: no Close, mode restoration or test cleanup
	// runs. Only the inherited original OFD can fence the surviving helper.
	os.Exit(17)
}

func TestNativeParentDeathCannotRepairBeforeInheritedLeaseEnds(t *testing.T) {
	f := modeNativeFixtureV1(t, false)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parentFile, childFile := os.NewFile(uintptr(fds[0]), "fixture-parent"), os.NewFile(uintptr(fds[1]), "fixture-owner")
	connection, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	parent := connection.(*net.UnixConn)
	defer parent.Close()
	defer childFile.Close()
	if err := parent.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := proto.EnableUnixCredentialsV1(parent); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestExecModeNativeSubprocessV1$")
	command.Env = append(os.Environ(), "MODE_NATIVE_TEST_ROLE=owner", "MODE_NATIVE_TEST_DIR="+f.dir)
	command.ExtraFiles = []*os.File{childFile}
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	wait := func() error { joined = true; return command.Wait() }
	defer func() {
		if !joined {
			command.Process.Kill()
			_ = wait()
		}
	}()
	childFile.Close()
	var one [1]byte
	n, control, err := proto.ReceiveUnixControlV1(parent, one[:], 2)
	defer control.Close()
	if err != nil || n != 1 || one[0] != 'h' || len(control.Files) != 2 {
		t.Fatal("native fixture did not transfer original child resources", err)
	}
	if err := control.CheckReceivedV1(parent); err != nil {
		t.Fatal(err)
	}
	files, err := control.DetachFilesV1()
	if err != nil {
		t.Fatal(err)
	}
	process, lifetime := files[0], files[1]
	defer lifetime.Close()
	defer func() { _ = unix.PidfdSendSignal(int(process.Fd()), unix.SIGKILL, nil, 0); process.Close() }()
	err = wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatal("fixture owner did not actually exit before recovery", err)
	}
	if guard, err := TryRuntimeModeGuard(t.Context(), f.artifacts, f.modes, f.item); !errors.Is(err, ErrRuntimeModeBusy) || guard != nil {
		if guard != nil {
			guard.Close()
		}
		t.Fatal("parent death released an inherited future-exec fence", err)
	}
	lifetime.Close()
	poll := []unix.PollFd{{Fd: int32(process.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 3000); err != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 {
		t.Fatal("original native holder did not physically end", err)
	}
	guard, err := TryRuntimeModeGuard(t.Context(), f.artifacts, f.modes, f.item)
	if err != nil || guard == nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := guard.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := f.file.Stat()
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Fatal("fresh exclusivity and exact intent did not restore the original inode", err)
	}
}
