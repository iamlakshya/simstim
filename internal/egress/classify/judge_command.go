package classify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type commandJudge struct {
	program  string
	maxScope string
}

var errCommand = errors.New("judge program did not return a bounded JSON answer. Try: check the configured executable and its input and output protocol")

// NewCommand selects one trusted executable, without shell interpretation.
// Arguments belong in a wrapper program. No session secret enters its env.
func NewCommand(program, maxScope string) (Judge, error) {
	if !filepath.IsAbs(program) || !textData(program, 4096) || maxScope != "once" && maxScope != "session" || !commandSupported() {
		return nil, errors.New("judge command needs an absolute executable path on a supported host. Try: configure a host program and once or session max_scope")
	}
	return &commandJudge{program: program, maxScope: maxScope}, nil
}

func (judge *commandJudge) Decide(ctx context.Context, input Input) (Result, error) {
	raw, err := EncodeInput(input)
	if err != nil {
		return Result{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		return Result{}, errCommand
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return Result{}, errCommand
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	cmd := exec.Command(judge.program) //nolint:gosec // The trusted config selects an exact executable, without a shell.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(append(raw, '\n'))
	cmd.Stdout = writer
	cmd.WaitDelay = 250 * time.Millisecond
	process := commandGroup(cmd)
	if cmd.Start() != nil {
		process.close()
		return Result{}, errCommand
	}
	defer process.close()
	_ = writer.Close()
	// No Wait runs until both stop paths finish. The unreaped owned leader
	// pins its process group while cancellation and cleanup target it.
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { process.stop(); _ = reader.Close() }) }
	cancelled := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() { stop(); close(cancelled) })
	answer, readErr := io.ReadAll(io.LimitReader(reader, MaxAnswerBytes+1))
	var exitErr error
	if readErr == nil && len(answer) <= MaxAnswerBytes {
		exitErr = process.waitExited()
	}
	if !stopCancellation() {
		<-cancelled
	}
	// A program must close stdout after its sole answer. Kill every remaining
	// member before reaping, including children that close their inherited IO.
	stop()
	waitErr := cmd.Wait()
	if ctx.Err() != nil || readErr != nil || exitErr != nil || len(answer) > MaxAnswerBytes {
		return Result{}, errCommand
	}
	if waitErr != nil {
		return Result{}, errCommand
	}
	return decodeResult(ctx, answer, judge.maxScope)
}

type commandProcess interface {
	waitExited() error
	stop()
	close()
}
