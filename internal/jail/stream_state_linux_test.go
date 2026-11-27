package jail

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
)

func TestConcurrentPipeReservationHasOneOwner(t *testing.T) {
	for _, stream := range []string{"stdin", "stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			c := lifecycleCmd(t)
			defer c.closeEnds(true)
			start := make(chan struct{})
			results := make(chan io.Closer, 16)
			var wg sync.WaitGroup
			for range 16 {
				wg.Go(func() {
					<-start
					var pipe io.Closer
					var err error
					switch stream {
					case "stdin":
						pipe, err = c.StdinPipe()
					case "stdout":
						pipe, err = c.StdoutPipe()
					case "stderr":
						pipe, err = c.StderrPipe()
					}
					if err == nil {
						results <- pipe
					} else if hintOf(err) == "" {
						t.Errorf("pipe reservation returned an error without a hint: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()
			close(results)
			owners := 0
			for pipe := range results {
				owners++
				_ = pipe.Close()
			}
			if owners != 1 {
				t.Errorf("%d callers reserved the same stream, want one", owners)
			}
		})
	}
}

func TestTypedNilFilesAreUnsetForPipesAndCombinedOutput(t *testing.T) {
	var file *os.File
	c := lifecycleCmd(t)
	c.Stdin, c.Stdout, c.Stderr = file, file, file
	defer c.closeEnds(true)
	for name, pipe := range map[string]func() (io.Closer, error){
		"stdin":  func() (io.Closer, error) { return c.StdinPipe() },
		"stdout": func() (io.Closer, error) { return c.StdoutPipe() },
		"stderr": func() (io.Closer, error) { return c.StderrPipe() },
	} {
		end, err := pipe()
		if err != nil {
			t.Errorf("typed-nil %s refused a pipe: %v", name, err)
		} else {
			_ = end.Close()
		}
	}
	c = lifecycleCmd(t)
	c.Stdout, c.Stderr = file, file
	out, err := c.CombinedOutput()
	var ended *ExitError
	if len(out) != 0 || !errors.As(err, &ended) || ended.Code != 7 {
		t.Errorf("typed-nil CombinedOutput = %q, %v, want command exit 7", out, err)
	}
}

type lockCheckingContext struct {
	context.Context
	cmd      *Cmd
	unlocked bool
}

func (c *lockCheckingContext) Err() error {
	if c.cmd.mu.TryLock() {
		c.cmd.mu.Unlock()
		c.unlocked = true
	}
	return nil
}

func TestToRunningChecksContextUnderSignalLock(t *testing.T) {
	ctx := &lockCheckingContext{Context: context.Background()}
	c := &Cmd{ctx: ctx, phase: phaseSetup, abort: make(chan struct{})}
	ctx.cmd = c
	if err := c.toRunning(); err != nil {
		t.Fatal(err)
	}
	if ctx.unlocked {
		t.Error("toRunning checked cancellation without excluding Signal")
	}
}
