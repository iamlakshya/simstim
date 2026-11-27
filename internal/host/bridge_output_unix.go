//go:build linux || darwin

package host

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
)

type processBridgeOutput struct {
	*processBridgeIO
	w     *proto.Writer
	r     *bufio.Reader
	write sync.Mutex
}

// NewProcessBridgeOutput transfers stdout to one trusted output-only child.
// This is an actual process-owned file, never a borrowed writer adapter.
// Killing its unreaped child interrupts even a blocking inherited pipe write.
func NewProcessBridgeOutput(executable string, stdout *os.File) (OwnedBridgeOutput, error) {
	return startBridgeOutput(executable, []string{"bridge-output"}, stdout)
}

func startBridgeOutput(executable string, args []string, stdout *os.File, fixtureFiles ...*os.File) (OwnedBridgeOutput, error) {
	owner, err := startBridgeIO(executable, args, stdout, false, fixtureFiles...)
	if err != nil {
		return nil, err
	}
	return &processBridgeOutput{processBridgeIO: owner, w: proto.NewWriter(owner.conn), r: bufio.NewReader(owner.conn)}, nil
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
