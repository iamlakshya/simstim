//go:build linux

package microvm

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/vmneed"
	"golang.org/x/sys/unix"
)

func nativeReadyFixture(t *testing.T) (*runnerControl, net.Conn, *ownedVMMEndpoint, *bufferedVsock, func()) {
	t.Helper()
	owner, path, stop := endpointFixture(t)
	guest, err := owner.dial(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guest.Close() })
	parent, childFile, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	child, err := net.FileConn(childFile)
	childFile.Close()
	if err != nil {
		parent.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close(); child.Close() })
	spec := firecrackerFixture(t)
	control, err := newNativeRunnerControl(parent, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.close(); control.finish() })
	return control, child, owner, &bufferedVsock{Conn: guest, reader: bufio.NewReader(guest)}, stop
}

func TestNativeVMMPairRejectsClosedPeerWhileProcessLives(t *testing.T) {
	owner, path, _ := endpointFixture(t)
	connection, err := owner.dial(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	peer, err := duplicateNativeFile(connection.(*net.UnixConn), "owned-test-peer")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	owner.mu.Lock()
	fd, err := unix.FcntlInt(uintptr(owner.fd), unix.F_DUPFD_CLOEXEC, 0)
	owner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	process := os.NewFile(uintptr(fd), "owned-test-pidfd")
	defer process.Close()
	if err := checkNativeVMMPair(process, peer); err != nil {
		t.Fatal("live original endpoint was refused", err)
	}
	if _, err := connection.Write([]byte{'X'}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var observedErr error
	if err := raw.Control(func(fd uintptr) {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}}
		n, err := unix.Poll(poll, 1000)
		if err != nil || n != 1 || poll[0].Revents&unix.POLLRDHUP == 0 {
			observedErr = fmt.Errorf("fixture peer did not close: n=%d revents=%d err=%v", n, poll[0].Revents, err)
			return
		}
		var queued [1]byte
		n, _, err = unix.Recvfrom(int(fd), queued[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		if err != nil || n != 1 || queued[0] != 'q' {
			observedErr = fmt.Errorf("fixture did not retain unread bytes after close: n=%d err=%v", n, err)
		}
	}); err != nil || observedErr != nil {
		t.Fatal("could not establish the closed-with-data counterexample", err, observedErr)
	}
	if err := owner.live(); err != nil {
		t.Fatal("fixture closed its process instead of its peer", err)
	}
	if checkNativeVMMPair(process, peer) == nil {
		t.Fatal("an ended guest stream retained a native continuation binding")
	}
}

func TestNativeVMMReadyCarriesExactDescriptorPair(t *testing.T) {
	control, sender, owner, guest, _ := nativeReadyFixture(t)
	proof := control.native.(*receivedVMMProof)
	if err := newRunnerServer(sender).sendNativeReady(proof.session, "firecracker", owner, guest); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := control.waitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err := proof.check(); err != nil {
		t.Fatal("the received original kernel pair was not retained", err)
	}
	control.close()
	control.finish()
	if proof.check() == nil {
		t.Fatal("a closed native channel kept its VM binding live")
	}
}

func TestNativeVMMReadyRejectsAnotherSessionAndDriver(t *testing.T) {
	for _, wrong := range []string{"session", "driver"} {
		t.Run(wrong, func(t *testing.T) {
			control, sender, owner, guest, _ := nativeReadyFixture(t)
			proof := control.native.(*receivedVMMProof)
			session, driver := proof.session, "firecracker"
			if wrong == "session" {
				session = "other"
			} else {
				driver = "chv"
			}
			if err := newRunnerServer(sender).sendNativeReady(session, driver, owner, guest); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if control.waitReady(ctx) == nil || proof.check() == nil {
				t.Fatal("another original session or driver supplied native readiness")
			}
		})
	}
}

func TestNativeBindingRequiresSameConnectionNeedAndLifetime(t *testing.T) {
	control, sender, owner, guest, stop := nativeReadyFixture(t)
	proof := control.native.(*receivedVMMProof)
	if err := newRunnerServer(sender).sendNativeReady(proof.session, "firecracker", owner, guest); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := control.waitReady(ctx); err != nil {
		t.Fatal(err)
	}
	// The ordinary child supplies real kernel handles. These fields model the
	// production engine's completed Start/hello seam, not a VM boot test.
	spec := proof.spec
	connection := &ownedGuestLink{Conn: guest}
	s := &sandbox{driver: &Driver{kind: firecrackerBackend}, spec: spec, owner: t.Context(), command: &fakeVMProcess{}, started: true, helloChecked: true, control: control, connection: connection, done: make(chan struct{})}
	binding, err := s.NativeBindingV1(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	expected := vmneed.NeedV1{BootV1: spec.VM.Clone(), Driver: "firecracker", CPUs: spec.CPUs, DiskMax: spec.DiskMax}
	if binding.SessionID() != spec.ID || binding.Check(t.Context(), connection, expected) != nil {
		t.Fatal("original native binding rejected its own data and connection")
	}
	if binding.Check(t.Context(), &ownedGuestLink{Conn: guest}, expected) == nil {
		t.Fatal("a reconstructed wrapper supplied the original Start identity")
	}
	changed := expected.Clone()
	changed.Image.RootHash = strings.Repeat("f", 64)
	if binding.Check(t.Context(), connection, changed) == nil {
		t.Fatal("the original native VM supplied a different boot Need")
	}
	if (&NativeBindingV1{}).Check(t.Context(), connection, expected) == nil {
		t.Fatal("a public zero-value DTO minted native VM facts")
	}
	stop()
	if binding.Check(t.Context(), connection, expected) == nil {
		t.Fatal("a dead original VMM kept its continuation live")
	}
}
