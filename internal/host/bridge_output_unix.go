//go:build linux || darwin

package host

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
)

type processBridgeOutput struct {
	conn  net.Conn
	w     *proto.Writer
	r     *bufio.Reader
	cmd   *exec.Cmd
	write sync.Mutex
	mu    sync.Mutex
	gone  bool
	err   error
	once  sync.Once
	done  chan struct{}
}

// NewProcessBridgeOutput transfers stdout to one trusted output-only child.
// This is an actual process-owned file, never a borrowed writer adapter.
// Killing its unreaped child interrupts even a blocking inherited pipe write.
func NewProcessBridgeOutput(executable string, stdout *os.File) (OwnedBridgeOutput, error) {
	return startBridgeOutput(executable, []string{"bridge-output"}, stdout)
}

func startBridgeOutput(executable string, args []string, stdout *os.File, fixtureFiles ...*os.File) (OwnedBridgeOutput, error) {
	if stdout == nil {
		return nil, errors.New("the output helper has no transferred stdout")
	}
	defer stdout.Close()
	conn, peer, err := bridgeOutputPair()
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	cmd := exec.Command(executable, args...)
	cmd.Env = []string{}
	cmd.ExtraFiles = append([]*os.File{peer}, fixtureFiles...)
	cmd.SysProcAttr = DieWithParent(&syscall.SysProcAttr{})
	cmd.Stdout = stdout
	// No unbounded inherited stderr pipe or sensitive environment is needed.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		conn.Close()
		return nil, err
	}
	out := &processBridgeOutput{conn: conn, w: proto.NewWriter(conn), r: bufio.NewReader(conn), cmd: cmd, done: make(chan struct{})}
	go func() {
		// The same actual child stays unreaped until the gone latch revokes
		// every cancellation signal right. No saved/reused PID is signalled.
		_, _ = proc.AwaitStatus(cmd.Process.Pid)
		out.mu.Lock()
		out.gone = true
		out.mu.Unlock()
		err := cmd.Wait()
		out.mu.Lock()
		out.err = err
		out.mu.Unlock()
		conn.Close()
		close(out.done)
	}()
	return out, nil
}

func (o *processBridgeOutput) Write(p []byte) (int, error) {
	return o.WriteProgress(p, nil)
}

// WriteProgress reports only acknowledged raw pieces to a nonblocking scoped
// liveness callback. It never treats a timer or buffered IPC write as progress.
func (o *processBridgeOutput) WriteProgress(p []byte, progress func()) (int, error) {
	o.write.Lock()
	defer o.write.Unlock()
	defer o.conn.SetDeadline(time.Time{})
	completed := 0
	for len(p) > 0 {
		// Small private pieces renew the idle deadline only after a REAL raw
		// write. No whole RPC/export duration cap limits a progressing sink.
		piece := p[:min(len(p), 4096)]
		_ = o.conn.SetDeadline(time.Now().Add(10 * time.Second))
		if err := o.w.Write(proto.TOutput, piece); err != nil {
			_ = o.Cancel()
			return completed, err
		}
		frame, err := proto.Read(o.r)
		if err != nil || frame.Type != proto.TPong || len(frame.Payload) != 0 {
			_ = o.Cancel()
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return completed, err
		}
		completed += len(piece)
		p = p[len(piece):]
		if progress != nil {
			progress()
		}
	}
	return completed, nil
}

func (o *processBridgeOutput) Cancel() error {
	o.once.Do(func() {
		_ = o.conn.Close() // interrupt parent Write/ACK before joining it
		o.mu.Lock()
		if !o.gone {
			_ = o.cmd.Process.Kill()
		}
		o.mu.Unlock()
	})
	return nil
}

func (o *processBridgeOutput) Wait() error {
	<-o.done
	o.mu.Lock()
	defer o.mu.Unlock()
	var exit *exec.ExitError
	if errors.As(o.err, &exit) && exit.ProcessState.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL {
		return nil // explicit owned cancellation, already reported by Write
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

// RunProcessBridgeOutput is the private, output-only helper entrypoint.
func RunProcessBridgeOutput(stdout *os.File) error {
	file := os.NewFile(3, "bridge-output-peer")
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	// This observes the private parent's owned socket without reading or
	// changing its flags. It also covers Darwin, where Pdeathsig is absent,
	// while the sole output thread is blocked in an inherited pipe write.
	watch := proc.WatchReadPeerGone(conn, func() { os.Exit(0) })
	if watch == nil {
		return errors.New("the output helper cannot watch its owning parent")
	}
	defer watch.Close()
	return RunBridgeOutput(conn, conn, stdout)
}
