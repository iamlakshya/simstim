//go:build linux || darwin

package host

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

type processBridgeInput struct {
	*processBridgeIO
	read    sync.Mutex
	r       *bufio.Reader
	pending []byte
}

// NewProcessBridgeInput transfers stdin to a single trusted input-only child.
// Cancellation closes the private socket and kills the original unreaped
// child, which interrupts an inherited blocking read without OFD flag changes.
func NewProcessBridgeInput(executable string, stdin *os.File) (OwnedBridgeInput, error) {
	return startBridgeInput(executable, []string{"bridge-input"}, stdin)
}

func startBridgeInput(executable string, args []string, stdin *os.File, fixtureFiles ...*os.File) (OwnedBridgeInput, error) {
	owner, err := startBridgeIO(executable, args, stdin, true, fixtureFiles...)
	if err != nil {
		return nil, err
	}
	return &processBridgeInput{processBridgeIO: owner, r: bufio.NewReader(owner.conn)}, nil
}

func (o *processBridgeInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	o.read.Lock()
	defer o.read.Unlock()
	o.mu.Lock()
	cancelled := o.cancelled
	o.mu.Unlock()
	if cancelled {
		return 0, net.ErrClosed
	}
	if len(o.pending) == 0 {
		frame, err := proto.ReadLimit(o.r, 4096+1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = o.conn.Close()
			} else {
				_ = o.Cancel()
			}
			return 0, err
		}
		if frame.Type != proto.TInput || len(frame.Payload) == 0 {
			_ = o.Cancel()
			return 0, UserError("protocol", "the input helper sent an invalid private frame", "Use the complete host bridge entrypoint.")
		}
		o.pending = frame.Payload
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancelled {
		o.pending = nil
		return 0, net.ErrClosed
	}
	n := copy(p, o.pending)
	o.pending = o.pending[n:]
	return n, nil
}

// RunProcessBridgeInput is the closed, input-only helper role. It protects
// memory before reading stdin and watches the same actual private parent
// socket, including while the sole input reader blocks on an inherited file.
func RunProcessBridgeInput() error {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	file := os.NewFile(3, "bridge-input-peer")
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	watch := proc.WatchReadPeerGone(conn, func() { os.Exit(0) })
	if watch == nil {
		return UserError("bridge_io", "the input helper cannot watch its owning parent", "Use the complete host bridge entrypoint.")
	}
	defer watch.Close()
	return runBridgeInput(os.Stdin, conn)
}

func runBridgeInput(in io.Reader, out io.Writer) error {
	w := proto.NewWriter(out)
	var data [4096]byte
	for {
		n, err := in.Read(data[:])
		if n < 0 || n > len(data) {
			return io.ErrUnexpectedEOF
		}
		if n > 0 {
			if writeErr := w.Write(proto.TInput, data[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}
