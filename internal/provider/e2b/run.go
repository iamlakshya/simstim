package e2b

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// inputWait is how long Run waits, after the command, for the copy of its
// input to end. Then Run leaves the copy, whose goroutine ends when its
// reader does, and returns the command's own result. os/exec instead waits
// for such a copy, and returns ErrWaitDelay.
var inputWait = 5 * time.Second

// endWait is how long a failed input waits for the command's end event,
// after its SIGKILL.
var endWait = 5 * time.Second

// runRead reads a command's output for Run. Tests replace it, to order the
// read against the end of ctx.
var runRead = func(p *proc) ([]byte, error) { return io.ReadAll(p.Stdout()) }

// Run runs remote to its end, with stdin, and returns its stdout. envd keeps
// a command running when its request drops, so the command does not outlive
// Run: Run kills it when its input fails, and when it has no exit status by
// the time Run returns. Run never reports success when the reader of the
// input failed before inputWait ran out. When only the sending failed, the
// command's own end wins: an exit with status 0 is a success, as the command
// then took what it needed. Its errors end with the command's stderr.
func (e *Envd) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	c, err := e.start(ctx, remote, startOptions{stderrTail: true})
	if err != nil {
		return nil, err
	}
	p := c.(*proc)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		p.send(ctx, stdin)
	}()
	out, rerr := runRead(p)
	werr := p.Wait()
	// A kill sends no signal when the exit status is known. When ctx has
	// ended, it waits for the kill that the end of ctx started.
	p.kill(ctx)
	t := time.NewTimer(inputWait)
	select {
	case <-sent:
	case <-t.C:
	}
	t.Stop()
	p.mu.Lock()
	ierr := p.inputErr
	p.mu.Unlock()
	tail := provider.Tail(p.stderrTail(), 3)
	// Keep one cancellation snapshot while choosing which error to return.
	cause := context.Cause(ctx)
	switch {
	case ierr == nil && werr == nil && rerr == nil:
		return out, nil
	case cause != nil && ierr != nil:
		// The CLI shows the input error's own message, so the tail goes in
		// there before the cause wraps it.
		return out, fmt.Errorf("%w: %w", cause, ierr.withTail(tail))
	case cause != nil:
		// A broken stream or a closed pipe is then the effect, not the cause.
		return out, withTail(fmt.Errorf("%w: %w", cause, firstErr(werr, rerr)), tail)
	case ierr != nil:
		return out, ierr.withTail(tail)
	case werr != nil:
		return out, withTail(werr, tail)
	default:
		return out, fmt.Errorf("read the command's output: %w", rerr)
	}
}

// withTail adds the end of the command's stderr to err, which says why it
// failed (the Transport's contract). An input error keeps its hint.
func withTail(err error, tail string) error {
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, tail)
}

// withTail is the input error with the end of the command's stderr in its
// message. It keeps its hint and its cause.
func (e *inputError) withTail(tail string) error {
	if tail == "" {
		return e
	}
	return &inputError{UserError: &client.UserError{Msg: e.Msg + ": " + tail, Hint: e.Hint}, err: e.err}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// source keeps the error of the reader that feeds a command's input, apart
// from the errors of sending it.
type source struct {
	r   io.Reader
	err error
}

func (s *source) Read(b []byte) (int, error) {
	n, err := s.r.Read(b)
	// Only io.EOF itself is the end, as for io.Copy: an error that wraps it
	// is the reader's failure.
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err //nolint:wrapcheck // a Reader passes its reader's errors on as they are
}

// send copies stdin to the command, then sends EOF, only when all of it was
// sent: a command never takes part of its input for all of it. A reader that
// fails part way gets the command killed. Sending that fails on its own while
// the command runs gets it killed too, as it would wait for the rest for
// ever. A failure after a kill started, or after ctx ended, is their doing.
func (p *proc) send(ctx context.Context, stdin io.Reader) {
	var err error
	if stdin != nil {
		src := &source{r: stdin}
		_, err = io.Copy(p.in, src)
		if src.err != nil {
			p.failInput(ctx, &inputError{
				UserError: &client.UserError{
					Msg:  fmt.Sprintf("cannot read the input of a command for the sandbox %s: %v", p.e.SandboxID, src.err),
					Hint: "run the command again",
				},
				err: src.err,
			})
			return
		}
	}
	if err == nil {
		err = p.in.Close()
	}
	// A failure after a kill started, or after ctx ended, is their doing.
	if err == nil || p.killing.Load() || ctx.Err() != nil {
		return
	}
	// The command may have ended on its own, before envd passed on its end,
	// and so made the input fail. Kill it, as it may also wait for the rest
	// of its input for ever, but keep its stream: the end event then says
	// how it ended, and the command's own end wins.
	p.signal(ctx)
	t := time.NewTimer(endWait)
	select {
	case <-p.done:
	case <-t.C:
	}
	t.Stop()
	if p.endedOnItsOwn() {
		return
	}
	p.failInput(ctx, &inputError{
		UserError: &client.UserError{
			Msg:  fmt.Sprintf("cannot send the input of a command to the sandbox %s: %v", p.e.SandboxID, err),
			Hint: p.e.ReachHint(),
		},
		err: err,
	})
}

// failInput keeps the first input error and kills the command.
func (p *proc) failInput(ctx context.Context, err *inputError) {
	p.mu.Lock()
	if p.inputErr == nil {
		p.inputErr = err
	}
	p.mu.Unlock()
	p.kill(ctx)
}

// inputError is a command's input that failed. It is a UserError, with a
// hint, and it keeps its cause.
type inputError struct {
	*client.UserError
	err error
}

func (e *inputError) Unwrap() []error { return []error{e.UserError, e.err} }

// exited says that the command's exit status is known.
func (p *proc) exited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit != nil
}

// endedOnItsOwn says that the command has an exit status of its own: envd
// gives a command that a signal killed the code -1.
func (p *proc) endedOnItsOwn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit != nil && *p.exit >= 0
}
