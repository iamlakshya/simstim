//go:build linux || darwin

package host

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proc"
)

// processBridgeIO owns one transferred stdio file in one actual child. The
// input role leaves naturally completed socket data for Read to drain.
type processBridgeIO struct {
	conn       net.Conn
	cmd        *exec.Cmd
	input      bool
	mu         sync.Mutex
	gone       bool
	cancelled  bool
	killIssued bool
	cancelErr  error
	observeErr error
	err        error
	once       sync.Once
	done       chan struct{}
}

func startBridgeIO(executable string, args []string, stdio *os.File, input bool, fixtureFiles ...*os.File) (*processBridgeIO, error) {
	return startBridgeIOObserved(executable, args, stdio, input, proc.AwaitStatusError, fixtureFiles...)
}

func startBridgeIOObserved(executable string, args []string, stdio *os.File, input bool, observe func(int) (syscall.WaitStatus, bool, error), fixtureFiles ...*os.File) (*processBridgeIO, error) {
	if stdio == nil {
		return nil, UserError("bridge_io", "the bridge I/O helper has no transferred file", "Use the complete host bridge entrypoint.")
	}
	defer stdio.Close()
	if observe == nil {
		return nil, UserError("bridge_io", "the bridge I/O helper has no native child observer", "Use the complete host bridge entrypoint.")
	}
	conn, peer, err := bridgeOutputPair()
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	cmd := exec.Command(executable, args...)
	cmd.Env = []string{}
	cmd.ExtraFiles = append([]*os.File{peer}, fixtureFiles...)
	cmd.SysProcAttr = DieWithParent(&syscall.SysProcAttr{})
	if input {
		cmd.Stdin = stdio
	} else {
		cmd.Stdout = stdio
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		conn.Close()
		return nil, err
	}
	owner := &processBridgeIO{conn: conn, cmd: cmd, input: input, done: make(chan struct{})}
	go func() {
		_, known, observeErr := observe(cmd.Process.Pid)
		if !known || observeErr != nil {
			if observeErr == nil {
				observeErr = errors.New("the original bridge child exit is unverified")
			}
			// Uncertainty is not exit. Revoke I/O and signal the original
			// child under its signal latch BEFORE any potentially reaping
			// Wait. Keep that latch held across the uncertain Wait, so no
			// concurrent cancellation can signal a PID after it is reaped.
			_ = owner.Cancel()
			owner.mu.Lock()
			owner.observeErr = fmt.Errorf("observe the original bridge child exit: %w", errors.Join(observeErr, owner.cancelErr))
			owner.err = cmd.Wait()
			owner.gone = true
			owner.mu.Unlock()
		} else {
			// Native exit is known while the SAME child is still unreaped.
			// Revoke its signal right before performing the actual Wait.
			owner.mu.Lock()
			owner.gone = true
			owner.mu.Unlock()
			err := cmd.Wait()
			owner.mu.Lock()
			owner.err = err
			owner.mu.Unlock()
		}
		if !input {
			conn.Close()
		}
		close(owner.done)
	}()
	return owner, nil
}

func (o *processBridgeIO) Cancel() error {
	o.once.Do(func() {
		_ = o.conn.Close()
		o.mu.Lock()
		o.cancelled = true
		if !o.gone {
			if err := o.cmd.Process.Kill(); err != nil {
				o.cancelErr = fmt.Errorf("stop the actual bridge I/O child: %w", err)
			} else {
				o.killIssued = true
			}
		}
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.input {
		return nil // Preserve the existing output cancellation API.
	}
	return o.cancelErr
}

func (o *processBridgeIO) Wait() error {
	<-o.done
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.observeErr != nil {
		return errors.Join(o.observeErr, o.err)
	}
	var exit *exec.ExitError
	if errors.As(o.err, &exit) && (!o.input || o.killIssued) {
		if status, ok := exit.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signal() == syscall.SIGKILL {
			return nil
		}
	}
	return o.err
}

func bridgeOutputPair() (net.Conn, *os.File, error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fds[0]), "bridge-output")
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		syscall.Close(fds[1])
		return nil, nil, err
	}
	return conn, os.NewFile(uintptr(fds[1]), "bridge-output-peer"), nil
}
