package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// RootRunOptions transfers terminal input only after its previous reader joins.
// Passwordless must match the original request sealed by the LOCALROOT owner.
type RootRunOptions struct {
	Passwordless bool
	In           *os.File
	Out          io.Writer
}

// runRootStepExecution accepts only the opaque LOCALROOT execution created by
// RunRootStep. It has no SSH command builder or arbitrary-command fallback.
func runRootStepExecution(ctx context.Context, execution *rootStepExecution, o RootRunOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if execution == nil {
		return rootStepError("the root step has no original execution owner")
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stderr
	}
	if err := keyread.Release(o.In); err != nil {
		return &UserError{Msg: "the terminal's previous reader did not stop", Hint: "Open a new terminal and run the command again.", Code: "terminal_busy", Kind: KindFailed}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.Passwordless {
		process, err := execution.start(ctx, rootStepPasswordlessProbe, o)
		if err != nil {
			return err
		}
		if err := waitRootStepProcess(ctx, process); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seen := &rootMarkerWriter{out: o.Out}
	o.Out = seen
	process, err := execution.start(ctx, rootStepApply, o)
	if err != nil {
		return err
	}
	err = waitRootStepProcess(ctx, process)
	return rootStepApplyResult(ctx, err, seen.done())
}

// This is the actual apply result path after the same owned Wait has joined.
// Marker observation is data only; cancellation retains the native error.
func rootStepApplyResult(ctx context.Context, err error, complete bool) error {
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	if err != nil || !complete {
		cause := "the completion marker was missing"
		if err != nil {
			cause = CleanLine(err.Error())
		}
		return &UserError{Msg: "the root step did not finish: " + cause, Hint: "Run the host doctor and keep the host's undo script; earlier steps may have worked.", Code: "root_step_failed", Kind: KindFailed}
	}
	return nil
}

// waitRootStepProcess waits for the same owned process even after cancellation.
// Cancellation cannot manufacture native exit or release its source lease early.
func waitRootStepProcess(ctx context.Context, process rootStepOwnedProcess) error {
	if process == nil {
		return rootStepError("the root step returned no owned native process")
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		process.Kill()
		return errors.Join(ctx.Err(), <-done)
	}
}

// rootMarkerWriter forwards exact bytes while observing only bounded lines.
// An incomplete, prefixed or overlong marker cannot confirm an execution.
type rootMarkerWriter struct {
	mu                 sync.Mutex
	out                io.Writer
	line               []byte
	overlong, complete bool
}

func (w *rootMarkerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(p)
	if n < 0 || n > len(p) {
		return 0, fmt.Errorf("invalid root-step terminal write count: %w", io.ErrShortWrite)
	}
	for _, b := range p[:n] {
		if b == '\n' {
			if !w.overlong && strings.TrimSuffix(string(w.line), "\r") == "__ROOT_STEP_DONE__" {
				w.complete = true
			}
			w.line, w.overlong = w.line[:0], false
		} else if len(w.line) < 64 && !w.overlong {
			w.line = append(w.line, b)
		} else {
			w.overlong = true
		}
	}
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, fmt.Errorf("write root-step terminal output: %w", err)
	}
	return n, nil
}

func (w *rootMarkerWriter) done() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.complete }

// VerifyRootSteps checks a fresh probe and its persistent step receipts. It is
// a result check, never permission for another root operation or driver start.
func VerifyRootSteps(before, after api.CapProbe, rs RootScript) error {
	if after.User != before.User || after.UID != before.UID {
		return rootStepError("the host account changed during the root step")
	}
	for _, step := range rs.Steps {
		if !slices.Contains(after.RootSteps, string(step.ID)) {
			return rootStepError("the host did not record root step " + string(step.ID))
		}
		ok := false
		switch step.ID {
		case RootKVMACL:
			ok = after.KVM.Usable && after.KVM.ACLUser
		case RootKVMGroup:
			ok = after.KVM.InGroup
		case RootLinger:
			ok = after.Linger
		case RootDelegateIO:
			ok = slices.Contains(after.Delegated, "io")
		case RootSubuid:
			ok = after.Subuid
		case RootIOUringOff:
			ok = after.IOURingDisabled == 2
		case RootVsockModules:
			ok = after.VsockBlocked
		}
		if !ok {
			return &UserError{Msg: "the fresh probe did not confirm root step " + string(step.ID), Hint: "Run the host doctor and keep the host's undo script.", Code: "root_step_unconfirmed", Kind: KindFailed}
		}
	}
	return nil
}
