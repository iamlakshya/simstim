package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// SIGINT and SIGTERM cancel a run: the command's context ends, the client
// prints the cancel and closes its debug file, and the run exits with 130
// (docs/contributing/cli-spec.md 5.1). An attach puts the terminal in raw
// mode, so there Ctrl+C is a byte for the agent, not a signal.

var (
	// interrupted closes at the first signal, so that a question that waits
	// for a line gives up.
	interrupted     = make(chan struct{})
	interruptedOnce sync.Once
)

// listen returns a context that ends at the first SIGINT or SIGTERM, and a
// function that stops listening. After the first signal, a second one
// ends the client at once, as it did before.
func listen() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
			signal.Stop(sigs)
			endAtSignal(cancel)
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(sigs)
		cancel()
	}
}

// endAtSignal ends the command's context with cancel, and then closes
// interrupted. The order matters: a command that sees interrupted closed
// can return before this goroutine runs on, and Main reads the end of the
// context to give 130.
func endAtSignal(cancel context.CancelFunc) {
	cancel()
	interruptedOnce.Do(func() { close(interrupted) })
}
