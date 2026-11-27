//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

// Only native code/namespace identity is exercised. This helper owns no
// session, OP, private artifact, boot receipt, guest or namespace factory.
func TestNativeHelperIdentityChildV1(t *testing.T) {
	if os.Getenv("NATIVE_IDENTITY_TEST_CHILD") != "1" {
		return
	}
	if err := secretmem.Protect(); err != nil {
		t.Fatal(err)
	}
	state, err := secretmem.Inspect()
	if err != nil || state.Dumpable == nil || *state.Dumpable != 0 || state.CoreHard != 0 {
		t.Fatalf("actual child protection: %+v %v", state, err)
	}
	address := os.Getenv("NATIVE_IDENTITY_TEST_ADDRESS")
	if address == "" {
		t.Fatal("the original parent omitted its test rendezvous")
	}
	// This actual protected child creates the connected endpoint. The parent's
	// SO_PEERCRED must identify this child, as its SCM sender does.
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	if err := line.WriteNativeHelloV1(proto.Hello{Role: NativeStartupRoleV1}); err != nil {
		t.Fatal(err)
	}
	frame, err := line.ReadFrame()
	if err != nil || frame.Type != proto.TPing {
		t.Fatal("original parent release", err)
	}
	if err := line.RejectAncillary(); err != nil {
		t.Fatal(err)
	}
}
func TestNativeHelperIdentityBindsActualND0ChildAndCannotRenewAfterExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	address := "@native-identity-" + rand.Text()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deadline, _ := ctx.Deadline()
	if err := listener.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	// Set PASSCRED before connect/send, so even the first queued hello retains
	// its original kernel credential control. Accepted sockets inherit it.
	raw, err := listener.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err := raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }); err != nil || optionErr != nil {
		t.Fatal(errors.Join(err, optionErr))
	}
	descriptor := -1
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeHelperIdentityChildV1$")
	command.Dir = "/"
	command.Env = append(os.Environ(), "NATIVE_IDENTITY_TEST_CHILD=1", "NATIVE_IDENTITY_TEST_ADDRESS="+address)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &descriptor}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if descriptor < 0 {
		t.Fatal("actual creation pidfd missing")
	}
	process, err := os.OpenRoot("/proc/" + strconv.Itoa(command.Process.Pid))
	if err != nil {
		_ = unix.Close(descriptor)
		t.Fatal(err)
	}
	original := &nativeStartupPeerV1{pid: command.Process.Pid, uid: uint32(os.Getuid()), pidfd: descriptor, proc: process}
	defer original.Close()
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	if err := line.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	frame, err := line.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var hello proto.Hello
	if json.Unmarshal(frame.Payload, &hello) != nil || hello.Role != NativeStartupRoleV1 {
		t.Fatal("child native purpose changed")
	}
	if err := line.AcceptNativeHelloV1(frame); err != nil {
		t.Fatal(err)
	}
	if err := line.bindOriginalNativePeerV1(original); err != nil {
		t.Fatal(err)
	}
	_, expected, err := mappedExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNativeHelperExecutableV1(original, expected, []string{os.Args[0], "-test.run=^TestNativeHelperIdentityChildV1$"}); err != nil {
		t.Fatal(err)
	}
	if err := line.WriteFrame(proto.TPing, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := line.bindOriginalNativePeerV1(original); err == nil {
		t.Fatal("exited original child was renewed from its old hello")
	}
}
func TestNativeHelperIdentityRefusesMissingNativeRoles(t *testing.T) {
	for _, identity := range []*nativeHelperIdentityV1{nil, {}} {
		if err := identity.Check(nil); err == nil {
			t.Fatal("metadata supplied a native helper identity")
		}
	}
	if err := checkNativeHelperExecutableV1(nil, nil, nil); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("missing original creation issued helper identity", err)
	}
}
