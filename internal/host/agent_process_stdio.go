package host

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/proto"
)

// RunAgentBridgeProcess serves ONLY the fixed one-connection helper entry.
// The caller MUST immediately exit this process with the returned code.
// Raw inherited stdio may remain blocked. It is owned by that process
// boundary, never borrowed by a reusable library or another operation.
func RunAgentBridgeProcess(in io.Reader, out io.Writer) int {
	return RunAgentBridgeProcessContext(context.Background(), in, out)
}

// RunAgentBridgeProcessContext adds an owned cancellation source to the
// SAME closed process path. After its return the caller must exit, with no
// host-library reuse. A memory fixture may only emit its final metric first.
func RunAgentBridgeProcessContext(ctx context.Context, in io.Reader, out io.Writer) int {
	if ctx == nil {
		return agentbridge.ProcessExitRefused
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return 0
	}
	if err := prepareAgentProcess(); err != nil {
		return agentbridge.ProcessExitRefused
	}
	if err := runAgentBridgeEntry(ctx, in, out, true); err != nil && !errors.Is(err, context.Canceled) {
		var cleanup *agentProcessCleanupError
		if errors.As(err, &cleanup) {
			return agentbridge.ProcessExitCleanup
		}
		return agentbridge.ProcessExitRefused
	}
	return 0
}

type agentIdentityRead struct {
	frame proto.Frame
	err   error
}

func readAgentProcessHostID(ctx context.Context, processOnly bool) (string, error) {
	if !processOnly {
		return HostID()
	}
	type identity struct {
		id  string
		err error
	}
	result := make(chan identity, 1)
	go func() {
		id, err := HostID()
		result <- identity{id: id, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-result:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return value.id, value.err
	}
}

func readAgentProcessHello(ctx context.Context, reader *bufio.Reader, processOnly bool) (proto.Frame, error) {
	if !processOnly {
		return proto.ReadLimit(reader, agentbridge.MaxIdentityHello)
	}
	result := make(chan agentIdentityRead, 1)
	go func() {
		frame, err := proto.ReadLimit(reader, agentbridge.MaxIdentityHello)
		result <- agentIdentityRead{frame: frame, err: err}
	}()
	select {
	case <-ctx.Done():
		return proto.Frame{}, ctx.Err()
	case value := <-result:
		if err := ctx.Err(); err != nil {
			return proto.Frame{}, err
		}
		return value.frame, value.err
	}
}

func writeAgentProcessHello(ctx context.Context, writer *proto.Writer, hello proto.Hello, processOnly bool) error {
	if !processOnly {
		return writer.WriteJSON(proto.THello, hello)
	}
	result := make(chan error, 1)
	go func() { result <- writer.WriteJSON(proto.THello, hello) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if stopped := ctx.Err(); stopped != nil {
			return stopped
		}
		return err
	}
}
