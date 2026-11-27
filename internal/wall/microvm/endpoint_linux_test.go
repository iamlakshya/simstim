//go:build linux

package microvm

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// This is only an owned ordinary subprocess. It starts no VMM, jail or namespace.
func TestOwnedVMMEndpointFixture(t *testing.T) {
	if os.Getenv("VM_ENDPOINT_FIXTURE") != "1" {
		return
	}
	listener, err := net.Listen("unix", os.Getenv("VM_ENDPOINT_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ready := os.NewFile(3, "ready")
	if _, err := ready.Write([]byte{'R'}); err != nil {
		t.Fatal(err)
	}
	ready.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var request [1]byte
				if n, _ := connection.Read(request[:]); n > 0 {
					if request[0] == 'X' {
						_, _ = connection.Write([]byte("queued"))
					} else {
						_, _ = io.Copy(io.Discard, connection)
					}
				}
			}()
		}
	}()
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func endpointFixture(t *testing.T) (*ownedVMMEndpoint, string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "vm-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestOwnedVMMEndpointFixture$")
	command.Env = append(os.Environ(), "VM_ENDPOINT_FIXTURE=1", "VM_ENDPOINT_SOCKET="+path)
	command.ExtraFiles = []*os.File{writer}
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	fd := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	if fd < 0 {
		command.Process.Kill()
		command.Wait()
		t.Fatal("ordinary child has no creation pidfd")
	}
	owner := &ownedVMMEndpoint{fd: fd, pid: command.Process.Pid, uid: uint32(os.Geteuid())}
	ended := false
	stop := func() {
		if !ended {
			ended = true
			input.Close()
			if err := command.Wait(); err != nil {
				t.Error("ordinary endpoint fixture failed", err)
			}
		}
	}
	t.Cleanup(func() { stop(); owner.close() })
	var message [1]byte
	if _, err := io.ReadFull(reader, message[:]); err != nil || message[0] != 'R' {
		t.Fatal("ordinary endpoint fixture did not become ready", err)
	}
	return owner, path, stop
}

func TestOwnedVMMEndpointChecksCreationLifetimeAndPeer(t *testing.T) {
	owner, path, stop := endpointFixture(t)
	other, wrongPath, _ := endpointFixture(t)
	if err := owner.live(); err != nil {
		t.Fatal(err)
	}
	connection, err := owner.dial(t.Context(), path)
	if err != nil {
		t.Fatal("the original native peer was refused", err)
	}
	connection.Close()
	if connection, err := owner.dial(t.Context(), wrongPath); err == nil {
		connection.Close()
		t.Fatal("another same-UID process replaced the original VMM endpoint")
	}
	if err := other.live(); err != nil {
		t.Fatal("refusing a peer harmed its unrelated process owner", err)
	}
	stop()
	if owner.live() == nil {
		t.Fatal("a reaped original process still supplied endpoint authority")
	}
	if connection, err := owner.dial(t.Context(), wrongPath); err == nil {
		connection.Close()
		t.Fatal("a later process replaced the original creation handle")
	}
}

func TestOwnedVMMEndpointClosedOwnerAndCancellation(t *testing.T) {
	owner, path, _ := endpointFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if connection, err := owner.dial(ctx, path); err != context.Canceled {
		if connection != nil {
			connection.Close()
		}
		t.Fatal("cancelled native endpoint dial continued", err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	if owner.live() == nil {
		t.Fatal("a closed native owner remained live")
	}
	if err := owner.close(); err != nil {
		t.Fatal("closing the same native owner twice failed", err)
	}
}
