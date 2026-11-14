package e2b

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// cancelOnSample forces cancellation after an Err snapshot was read. Hiding
// values makes context.Cause use Err, as it does for other custom contexts.
type cancelOnSample struct {
	context.Context
	cancel context.CancelFunc
	armed  atomic.Bool
}

func (c *cancelOnSample) Err() error {
	err := c.Context.Err()
	if c.armed.CompareAndSwap(true, false) {
		c.cancel()
	}
	return err
}

func (c *cancelOnSample) Value(any) any { return nil }

type inputFailureAfterExit struct {
	read <-chan *proc
	ctx  *cancelOnSample
}

func (r *inputFailureAfterExit) Read([]byte) (int, error) {
	p := <-r.read
	<-p.done
	r.ctx.armed.Store(true)
	return 0, errDisk
}

// The command ends cleanly and its reader fails. Cancellation lands during
// the final context sample. The input error and its hint must survive.
func TestEnvdRunKeepsInputErrorWhenCancellationRacesItsResult(t *testing.T) {
	e, _ := envdFor(t, false)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelOnSample{Context: parent, cancel: cancel}
	read := make(chan *proc, 1)
	old := runRead
	t.Cleanup(func() { runRead = old })
	runRead = func(p *proc) ([]byte, error) {
		out, err := io.ReadAll(p.Stdout())
		read <- p
		return out, err
	}
	out, err := e.Run(ctx, "printf output; printf source-reason >&2", &inputFailureAfterExit{read: read, ctx: ctx})
	if !errors.Is(parent.Err(), context.Canceled) {
		t.Fatal("the context was not cancelled during error selection")
	}
	msg := rendered(err)
	if string(out) != "output" || !errors.Is(err, errDisk) || !strings.Contains(msg, "cannot read the input") || !strings.Contains(msg, "source-reason") || !strings.Contains(msg, "\nTry: run the command again") {
		t.Fatalf("Run=%q,%v, rendered=%q, want the reader failure, stderr and hint", out, err, msg)
	}
	if strings.Contains(err.Error(), "%!w(") {
		t.Fatalf("Run returned a nil error formatting marker: %v", err)
	}
}
