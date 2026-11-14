package guest

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
)

type ownedCommandIO struct {
	Input    io.Reader
	Out, Err io.Writer
	// AttachInput starts a cancellable reader after Start and returns its join.
	AttachInput func(io.WriteCloser) func()
	AbortOutput func()
}

// executeOwned is the one non-PTY command start/group/signal/wait path. Probe,
// bundle, Store and exec callers supply bounded owned IO, never host fallback.
func (g *Init) executeOwned(run *execRun, streams ownedCommandIO) int {
	cmd, code, err := g.agentCommand(run, streams.Out, streams.Err)
	if err != nil {
		commandDiagnostic(streams.Err, err)
		return code
	}
	if run.inputBundle != nil {
		if run.vmRole != vmSeedCommand || run.kind != "" {
			return 125
		}
		cmd.ExtraFiles = []*os.File{run.inputBundle}
	}
	var input io.WriteCloser
	if streams.AttachInput != nil {
		input, err = cmd.StdinPipe()
		if err != nil {
			commandDiagnostic(streams.Err, err)
			return 125
		}
	} else {
		cmd.Stdin = streams.Input
	}
	run.abortOutput = streams.AbortOutput
	if err = g.startOwned(run, cmd); err != nil {
		if input != nil {
			input.Close()
		}
		if run.ctx.Err() != nil {
			return execCancelled(run.ctx)
		}
		commandDiagnostic(streams.Err, err)
		return execStartCode(err)
	}
	var finishInput func()
	if streams.AttachInput != nil {
		finishInput = streams.AttachInput(input)
	}
	cancelled := make(chan struct{})
	go func() {
		select {
		case <-run.ctx.Done():
			_ = run.signal(uint32(syscall.SIGKILL))
			if streams.AbortOutput != nil {
				streams.AbortOutput()
			}
		case <-cancelled:
		}
	}()
	owned, err := g.waitOwned(run)
	close(cancelled)
	cause := run.ctx.Err() // joining an input reader may itself cancel the run
	if finishInput != nil {
		finishInput()
	}
	if cause != nil {
		if errors.Is(cause, context.DeadlineExceeded) {
			return 124
		}
		return 125
	}
	if !owned {
		commandDiagnostic(streams.Err, execError("the command's owned exit was lost"))
		return 125
	}
	return execWaitCode(err)
}
func commandDiagnostic(out io.Writer, err error) {
	if out != nil {
		_, _ = io.WriteString(out, err.Error()+"\nTry: check the guest command and run it again.\n")
	}
}
