//go:build linux

package microvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

type receivedVMMProof struct {
	mu            sync.Mutex
	session       string
	spec          wall.Spec
	pidfd, socket *os.File
	ready, closed bool
}

func cloneBindingSpec(spec wall.Spec) wall.Spec {
	if spec.VM != nil {
		copy := spec.VM.Clone()
		spec.VM = &copy
	}
	spec.Mounts = slices.Clone(spec.Mounts)
	spec.StateDirs, spec.StateFiles = slices.Clone(spec.StateDirs), slices.Clone(spec.StateFiles)
	return spec
}

func (p *receivedVMMProof) acceptReady(message runnerMessage, control *proto.UnixControlV1) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kind, err := backendForSpec(p.spec)
	if err != nil {
		return err
	}
	if p.ready || p.closed || message.Session != p.session || message.Driver != kind.name() || len(control.Files) != 2 || control.Credentials == nil {
		return fmt.Errorf("private VMM readiness has no exact original descriptor roles and session")
	}
	if err := checkNativeVMMPair(control.Files[0], control.Files[1]); err != nil {
		return err
	}
	files, err := control.DetachFilesV1()
	if err != nil {
		return err
	}
	p.pidfd, p.socket, p.ready = files[0], files[1], true
	return nil
}

func (p *receivedVMMProof) check() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.closed {
		return fmt.Errorf("the engine has no live original VMM descriptor pair")
	}
	return checkNativeVMMPair(p.pidfd, p.socket)
}

func (p *receivedVMMProof) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var failure error
	for _, file := range []*os.File{p.pidfd, p.socket} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	p.pidfd, p.socket = nil, nil
	return failure
}

func checkNativeVMMPair(pidfd, socket *os.File) error {
	if pidfd == nil || socket == nil {
		return fmt.Errorf("the native VMM descriptor pair is incomplete")
	}
	pid, err := livePIDFDTarget(pidfd)
	if err != nil {
		return err
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var nativeErr error
	err = raw.Control(func(fd uintptr) {
		kind, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || kind != unix.SOCK_STREAM {
			nativeErr = errors.Join(err, fmt.Errorf("the VMM peer is not a native stream"))
			return
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLRDHUP}}
		for {
			_, err := unix.Poll(poll, 0)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLRDHUP|unix.POLLNVAL) != 0 {
				nativeErr = errors.Join(err, fmt.Errorf("the original host-side guest stream is closed or uncertain"))
				return
			}
			break
		}
		if _, err := unix.Getsockname(int(fd)); err != nil {
			nativeErr = err
			return
		}
		peer, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || peer == nil || int(peer.Pid) != pid || peer.Uid != uint32(os.Geteuid()) {
			nativeErr = errors.Join(err, fmt.Errorf("the host-side guest stream has another native VMM peer"))
			return
		}
		// Probe EOF without consuming one guest byte. Readability can be real
		// queued data, so POLLIN alone is not a disconnect or a readiness grant.
		var peek [1]byte
		for {
			n, _, err := unix.Recvfrom(int(fd), peek[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == nil && n > 0 {
				break
			}
			nativeErr = errors.Join(err, fmt.Errorf("the original host-side guest stream ended"))
			break
		}
	})
	if err != nil || nativeErr != nil {
		return errors.Join(err, nativeErr)
	}
	again, err := livePIDFDTarget(pidfd)
	if err != nil || again != pid {
		return errors.Join(err, fmt.Errorf("the original VMM changed lifetime during peer validation"))
	}
	return nil
}

func livePIDFDTarget(file *os.File) (int, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid := 0
	var nativeErr error
	err = raw.Control(func(fd uintptr) {
		// Signal zero queries this exact native handle. No numeric PID is
		// opened, signalled or adopted from the following correlation data.
		if err := unix.PidfdSendSignal(int(fd), 0, nil, 0); err != nil {
			nativeErr = err
			return
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			n, err := unix.Poll(poll, 0)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n != 0 || poll[0].Revents != 0 {
				nativeErr = errors.Join(err, fmt.Errorf("the native VMM is no longer live"))
				return
			}
			break
		}
		info, err := os.Open("/proc/self/fdinfo/" + strconv.FormatUint(uint64(fd), 10))
		if err != nil {
			nativeErr = err
			return
		}
		data, readErr := io.ReadAll(io.LimitReader(info, 4097))
		var filesystem unix.Statfs_t
		statErr := unix.Fstatfs(int(info.Fd()), &filesystem)
		closeErr := info.Close()
		if readErr != nil || closeErr != nil || statErr != nil || filesystem.Type != unix.PROC_SUPER_MAGIC || len(data) > 4096 {
			nativeErr = errors.Join(readErr, closeErr, statErr, fmt.Errorf("native pidfd correlation is not bounded actual procfs metadata"))
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Pid:") {
				if pid != 0 {
					nativeErr = fmt.Errorf("native pidfd correlation repeats its PID")
					return
				}
				value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pid:")))
				if err != nil || value <= 0 {
					nativeErr = fmt.Errorf("native pidfd correlation has no live namespace PID")
					return
				}
				pid = value
			}
		}
		if pid == 0 {
			nativeErr = fmt.Errorf("native handle is not an identifiable pidfd")
		}
	})
	return pid, errors.Join(err, nativeErr)
}

type sandboxNativeBinding struct {
	sandbox    *sandbox
	connection *ownedGuestLink
	proof      *receivedVMMProof
}

func (b *sandboxNativeBinding) sessionID() string { return b.proof.session }

func (b *sandboxNativeBinding) checkNativeBinding(ctx context.Context, connection net.Conn, expected vmneed.NeedV1) error {
	actualLink, ok := connection.(*ownedGuestLink)
	if !ok || actualLink != b.connection {
		return fmt.Errorf("the VM continuation has another Start-returned connection")
	}
	s := b.sandbox
	s.mu.Lock()
	live := s.started && s.helloChecked && !s.forced && !s.destroyed && !s.destroying && !s.preparedReleased && !s.physicalExit && s.command != nil && s.connection == b.connection
	owner := s.owner
	s.mu.Unlock()
	if !live || owner == nil || owner.Err() != nil {
		return fmt.Errorf("the original engine-owned VM lifetime ended")
	}
	b.proof.mu.Lock()
	prepared := cloneBindingSpec(b.proof.spec)
	b.proof.mu.Unlock()
	kind, err := backendForSpec(prepared)
	if err != nil {
		return err
	}
	actual := vmneed.NeedV1{BootV1: prepared.VM.Clone(), Driver: kind.name(), CPUs: prepared.CPUs, DiskMax: prepared.DiskMax}
	left, err := vmneed.EncodeV1(actual, runtime.GOARCH)
	if err != nil {
		return err
	}
	right, err := vmneed.EncodeV1(expected, runtime.GOARCH)
	if err != nil || !bytes.Equal(left, right) {
		return errors.Join(err, fmt.Errorf("the native VM differs from the complete original Need"))
	}
	if err := b.proof.check(); err != nil {
		return err
	}
	return ctx.Err()
}

// NativeBindingV1 borrows facts from this actual running sandbox. No saved
// layout, arbitrary connection, process number or success callback can mint it.
func (s *sandbox) NativeBindingV1(ctx context.Context, connection net.Conn) (*NativeBindingV1, error) {
	owned, ok := connection.(*ownedGuestLink)
	if !ok {
		return nil, fmt.Errorf("the native VM binding needs its exact Start-returned stream")
	}
	s.mu.Lock()
	if s.control == nil || s.spec.VM == nil || s.connection != owned {
		s.mu.Unlock()
		return nil, fmt.Errorf("the sandbox has no original native VMM control")
	}
	proof, ok := s.control.native.(*receivedVMMProof)
	expected := vmneed.NeedV1{BootV1: s.spec.VM.Clone(), Driver: s.driver.Name(), CPUs: s.spec.CPUs, DiskMax: s.spec.DiskMax}
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("the sandbox did not receive its actual native VMM descriptors")
	}
	verifier := &sandboxNativeBinding{sandbox: s, connection: owned, proof: proof}
	if err := verifier.checkNativeBinding(ctx, connection, expected); err != nil {
		return nil, err
	}
	return &NativeBindingV1{verifier: verifier}, nil
}
