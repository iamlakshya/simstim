//go:build linux

package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
)

type sandbox struct {
	spec   wall.Spec
	mu     sync.Mutex
	cmd    *exec.Cmd
	pidfd  int
	link   *net.UnixConn
	exited chan struct{}
	closed bool
	forced bool
	result wall.Exit
}

func newSandbox(spec wall.Spec) (wall.Sandbox, error) {
	return &sandbox{spec: spec, pidfd: -1, exited: make(chan struct{})}, nil
}

// Start owns every descriptor and partial child before returning. The caller's
// context bounds the start, while the sandbox outlives a successful start.
func (s *sandbox) Start(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, setupError("sandbox start was cancelled", err)
	}
	plan, err := MountPlan(s.spec)
	if err != nil {
		return nil, setupError("cannot plan process mounts", err)
	}
	launcher, err := launcherPath(s.spec.Helper)
	if err != nil {
		return nil, err
	}
	rootPath := filepath.Join(s.spec.SessionDir, "process-root")
	if err = os.MkdirAll(rootPath, 0700); err != nil {
		return nil, setupError("cannot make private root mount point", err)
	}
	root, err := pinSource(rootPath)
	if err != nil {
		return nil, setupError("cannot pin private root mount point", err)
	}
	defer root.Close()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, setupError("cannot make guest link", err)
	}
	ours := os.NewFile(uintptr(fds[0]), "supervisor-link")
	theirs := os.NewFile(uintptr(fds[1]), "guest-link")
	defer theirs.Close()
	conn, err := net.FileConn(ours)
	ours.Close()
	if err != nil {
		return nil, setupError("cannot adopt guest link", err)
	}
	link, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, setupError("guest link is not a Unix stream", nil)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		link.Close()
		return nil, setupError("cannot make launcher request pipe", err)
	}
	defer reader.Close()
	defer writer.Close()
	extra := []*os.File{reader, theirs, root}
	req := Request{V: RequestVersion, Mounts: []MountRequest{}}
	for _, m := range plan {
		var f *os.File
		var e error
		if m.Identity != "" {
			f, e = files.PinGrant(m)
		} else {
			f, e = pinSource(m.Host)
		}
		if e != nil {
			for _, f := range extra[3:] {
				f.Close()
			}
			link.Close()
			return nil, setupError("cannot pin sandbox mount", e)
		}
		extra = append(extra, f)
		req.Mounts = append(req.Mounts, MountRequest{FD: len(extra) + 2, Guest: m.Guest, Write: m.Write})
	}
	defer func() {
		for _, f := range extra[3:] {
			f.Close()
		}
	}()
	logf, err := os.OpenFile(filepath.Join(s.spec.SessionDir, "namespace.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		link.Close()
		return nil, setupError("cannot open namespace log", err)
	}
	defer logf.Close()
	cmd := exec.Command(launcher)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = extra
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	s.mu.Lock()
	if s.closed || s.cmd != nil {
		s.mu.Unlock()
		link.Close()
		return nil, setupError("sandbox already started or closed", nil)
	}
	if err = ctx.Err(); err != nil {
		s.mu.Unlock()
		link.Close()
		return nil, setupError("sandbox start was cancelled", err)
	}
	if err = cmd.Start(); err != nil {
		s.mu.Unlock()
		link.Close()
		return nil, setupError("cannot start static namespace launcher", err)
	}
	s.cmd, s.link = cmd, link
	s.mu.Unlock()
	go s.reap(cmd)
	requestDone := make(chan error, 1)
	go func() {
		e := json.NewEncoder(writer).Encode(req)
		closeErr := writer.Close()
		if e == nil {
			e = closeErr
		}
		requestDone <- e
	}()
	abort := func(cause error) (net.Conn, error) {
		writer.Close()
		link.Close()
		_ = s.Kill()
		_ = s.Wait()
		return nil, setupError("namespace launcher did not start the sandbox", cause)
	}
	select {
	case err = <-requestDone:
		if err != nil {
			return abort(err)
		}
	case <-ctx.Done():
		return abort(ctx.Err())
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = link.SetReadDeadline(deadline)
	}
	received := make(chan error, 1)
	go func() { received <- s.adoptInit(link) }()
	select {
	case err = <-received:
		if err != nil {
			return abort(err)
		}
	case <-ctx.Done():
		link.Close()
		<-received
		return abort(ctx.Err())
	}
	_ = link.SetReadDeadline(time.Time{})
	return link, nil
}

func (s *sandbox) adoptInit(conn *net.UnixConn) error {
	data := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	n, oob, flags, _, err := conn.ReadMsgUnix(data, control)
	if err != nil {
		return fmt.Errorf("receive namespace-init ownership: %w", err)
	}
	msgs, err := unix.ParseSocketControlMessage(control[:oob])
	if err != nil {
		return fmt.Errorf("parse namespace-init ownership: %w", err)
	}
	var received []int
	for _, m := range msgs {
		fds, e := unix.ParseUnixRights(&m)
		if e != nil {
			for _, fd := range received {
				unix.Close(fd)
			}
			return fmt.Errorf("parse namespace-init descriptor: %w", e)
		}
		received = append(received, fds...)
	}
	if n != 1 || data[0] != 'P' || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(received) != 1 {
		for _, fd := range received {
			unix.Close(fd)
		}
		return fmt.Errorf("invalid namespace-init ownership message")
	}
	fd := received[0]
	unix.CloseOnExec(fd)
	// Verify this is a pidfd without delivering a signal. The launcher cannot
	// substitute a file, mount descriptor or arbitrary open socket.
	if err = unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		unix.Close(fd)
		return fmt.Errorf("validate namespace-init pidfd: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		unix.Close(fd)
		return fmt.Errorf("sandbox ended before ownership transfer")
	}
	s.pidfd = fd
	// Ownership is published before the acknowledgement. Kill and reap use
	// this same lock, so neither can release or overlook an acknowledged init.
	_, err = conn.Write([]byte{'A'})
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("acknowledge namespace-init ownership: %w", err)
	}
	return nil
}

func (s *sandbox) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	s.closed = true
	if s.pidfd >= 0 {
		if !initExited(s.pidfd) {
			s.forced = true
		}
		_ = unix.PidfdSendSignal(s.pidfd, unix.SIGKILL, nil, 0)
		waitInitExit(s.pidfd)
		_ = unix.Close(s.pidfd)
		s.pidfd = -1
	}
	s.result = wall.Exit{Err: err}
	if cmd.ProcessState != nil {
		s.result.Code = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			s.result.Signal = ws.Signal().String()
		}
	}
	close(s.exited)
	s.mu.Unlock()
}

// Stop terminates the namespace init after the caller's graceful ctl exchange.
func (s *sandbox) Stop(ctx context.Context, grace time.Duration) error {
	s.mu.Lock()
	if s.pidfd >= 0 {
		_ = unix.PidfdSendSignal(s.pidfd, unix.SIGTERM, nil, 0)
	}
	started := s.cmd != nil
	s.mu.Unlock()
	if !started {
		return nil
	}
	timer := time.NewTimer(max(grace, 0))
	defer timer.Stop()
	select {
	case <-s.exited:
		return nil
	case <-ctx.Done():
		_ = s.Kill()
		return ctx.Err()
	case <-timer.C:
		return s.Kill()
	}
}

// Kill ends the whole PID namespace through its retained init pidfd.
func (s *sandbox) Kill() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.pidfd >= 0 {
		if !initExited(s.pidfd) {
			s.forced = true
		}
		if err := unix.PidfdSendSignal(s.pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("kill namespace init: %w", err)
		}
	}
	// Before the acknowledged transfer the outer launcher owns the blocked
	// child. Closing its link refuses activation and its own cleanup kills it.
	if s.link != nil {
		_ = s.link.Close()
	}
	if s.cmd != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	} else {
		s.closed = true
	}
	return nil
}

// Wait publishes an end only after the namespace init has been killed.
func (s *sandbox) Wait() wall.Exit {
	s.mu.Lock()
	started := s.cmd != nil
	s.mu.Unlock()
	if started {
		<-s.exited
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

// Destroy removes only a stopped sandbox's private runtime files.
func (s *sandbox) Destroy() error {
	s.mu.Lock()
	running := s.cmd != nil && !s.closed
	s.mu.Unlock()
	if running {
		return setupError("stop the sandbox before destroying it", nil)
	}
	if err := os.RemoveAll(s.spec.SessionDir); err != nil {
		return setupError("cannot remove sandbox files", err)
	}
	return nil
}

// Abilities reports no optional copy or freeze support at this level.
func (s *sandbox) Abilities() wall.Abilities { return wall.Abilities{} }

// waitInitExit holds ownership until the namespace init has actually exited.
// Killing a pidfd is asynchronous. Poll readiness is the kernel's evidence,
// including when the outer launcher died before it could reap PID1.
func waitInitExit(fd int) {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(poll, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n > 0 {
			return
		}
	}
}

// CleanExit is trusted evidence for committing an already captured state epoch.
// It stays false until actual Wait completion, and after forced termination.
func (s *sandbox) CleanExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed && s.cmd != nil && s.result.Err == nil && s.result.Code == 0 && !s.forced
}

func initExited(fd int) bool {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	return err == nil && n > 0 && poll[0].Revents&unix.POLLIN != 0
}
