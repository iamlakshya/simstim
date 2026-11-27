package host

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

// OwnedBridgeOutput transfers cancellation and join rights explicitly. Cancel
// must unblock Write; Wait joins its actual owned resource after cancellation.
// An arbitrary borrowed writer does not satisfy that ownership contract.
type OwnedBridgeOutput interface {
	io.Writer
	Cancel() error
	Wait() error
}

// RunBridgeOwned serves a connection with an explicitly cancellable output.
// The process entrypoint supplies the real output owner before advertising pull.
func RunBridgeOwned(in io.Reader, out OwnedBridgeOutput) error {
	if out == nil {
		return fmt.Errorf("the bridge has no owned output. Try: use the complete host entrypoint")
	}
	defer out.Wait()
	defer out.Cancel()
	return runBridge(in, out, out)
}

// RunBridgeOutput copies only trusted parent-provided bytes to this process's
// sole stdout. It owns no session or command. Each ACK follows the complete
// raw write, so a successful parent Write means those bytes reached stdout.
func RunBridgeOutput(in io.Reader, ack io.Writer, out io.Writer) error {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	r, w := bufio.NewReader(in), proto.NewWriter(ack)
	for {
		frame, err := proto.ReadLimit(r, 4096+1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if frame.Type != proto.TOutput {
			return fmt.Errorf("the output helper received an invalid frame")
		}
		for len(frame.Payload) > 0 {
			n, err := out.Write(frame.Payload)
			if n < 0 || n > len(frame.Payload) {
				return io.ErrShortWrite
			}
			frame.Payload = frame.Payload[n:]
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
		if err := w.Write(proto.TPong, nil); err != nil {
			return err
		}
	}
}
