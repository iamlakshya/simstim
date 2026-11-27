//go:build linux

package host

import (
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A finite fixture for the PUBLIC bootstrap lifetime latch only. Neither the
// IPC nor these pidfds establish manager adoption or grant a private source.
func TestPurgeScopeParentDeathFixtureV3(t *testing.T) {
	switch os.Getenv("PURGE_ADOPTION_FIXTURE") {
	case "child":
		gate, parent := os.NewFile(3, "actual-public-adoption-latch"), os.NewFile(4, "actual-fork-parent-pidfd")
		if err := waitPurgeAttemptAdoptionV3(gate); err != nil {
			os.Exit(81)
		}
		if !errors.Is(unix.PidfdSendSignal(int(parent.Fd()), 0, nil, 0), unix.ESRCH) {
			os.Exit(82)
		}
		os.Exit(0)
	case "parent":
		gate := os.NewFile(3, "actual-public-adoption-latch")
		fd, err := unix.PidfdOpen(os.Getpid(), 0)
		if err != nil {
			os.Exit(83)
		}
		parent := os.NewFile(uintptr(fd), "actual-original-test-parent")
		childFD := -1
		child := exec.Command(os.Args[0], "-test.run=^TestPurgeScopeParentDeathFixtureV3$")
		child.Env = append(os.Environ(), "PURGE_ADOPTION_FIXTURE=child")
		child.ExtraFiles = []*os.File{gate, parent}
		child.Stdout, child.Stderr = io.Discard, io.Discard
		child.SysProcAttr = &syscall.SysProcAttr{PidFD: &childFD}
		if err := child.Start(); err != nil || childFD < 0 {
			os.Exit(84)
		}
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: os.Getenv("PURGE_ADOPTION_ADDRESS"), Net: "unix"})
		if err != nil {
			os.Exit(85)
		}
		if _, _, err := connection.WriteMsgUnix([]byte{1}, unix.UnixRights(childFD), nil); err != nil {
			os.Exit(86)
		}
		// Deliberate actual parent death while the child is waiting. Its own
		// parent does NOT Wait here, matching the raw bootstrap failure case.
		os.Exit(0)
	}
}

func TestPurgeScopeActualParentDeathKeepsBootstrapAliveUntilSettlementV3(t *testing.T) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	gate := os.NewFile(uintptr(fd), "actual-test-adoption-completion")
	defer gate.Close()
	defer func() { var complete [8]byte; complete[0] = 1; _, _ = gate.Write(complete[:]) }()
	address := "@purge-adoption-" + rand.Text()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	parent := exec.Command(os.Args[0], "-test.run=^TestPurgeScopeParentDeathFixtureV3$")
	parent.Env = append(os.Environ(), "PURGE_ADOPTION_FIXTURE=parent", "PURGE_ADOPTION_ADDRESS="+address)
	parent.ExtraFiles = []*os.File{gate}
	parentFD := -1
	parent.SysProcAttr = &syscall.SysProcAttr{PidFD: &parentFD}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	if parentFD < 0 {
		t.Fatal("the original fixture parent has no native creation")
	}
	defer unix.Close(parentFD)
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	data, control := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	n, oob, flags, _, err := connection.ReadMsgUnix(data, control)
	if err != nil || n != 1 || data[0] != 1 || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
		t.Fatal("actual creator omitted original child handle", err)
	}
	messages, err := unix.ParseSocketControlMessage(control[:oob])
	if err != nil || len(messages) != 1 {
		t.Fatal("actual creator child-handle framing", err)
	}
	rights, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(rights) != 1 {
		t.Fatal("actual creator child-handle role", err)
	}
	child := os.NewFile(uintptr(rights[0]), "actual-creator-child-pidfd")
	defer child.Close()
	if err := parent.Wait(); err != nil {
		t.Fatal(err)
	}
	pollEINTR := func(polls []unix.PollFd, ms int) (int, error) {
		deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
		for {
			rem := int(time.Until(deadline).Milliseconds())
			if rem < 1 {
				rem = 1
			}
			n, err := unix.Poll(polls, rem)
			if errors.Is(err, unix.EINTR) && time.Now().Before(deadline) {
				continue
			}
			return n, err
		}
	}
	polls := []unix.PollFd{{Fd: int32(child.Fd()), Events: unix.POLLIN}}
	if n, err := pollEINTR(polls, 50); err != nil || n != 0 {
		t.Fatalf("actual parent death freed bootstrap before settlement: %d %v", n, err)
	}
	var complete [8]byte
	complete[0] = 1
	if _, err := gate.Write(complete[:]); err != nil {
		t.Fatal(err)
	}
	if n, err := pollEINTR(polls, 5000); err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("settled bootstrap did not join after actual parent death: %d %v", n, err)
	}
}
