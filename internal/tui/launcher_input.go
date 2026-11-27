package tui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keyread"
)

var (
	launcherIn          = func() *os.File { return os.Stdin }
	launcherOut         = func() *os.File { return os.Stdout }
	launcherState       = term.GetState
	openLauncherReader  = uv.NewCancelReader
	launcherReleaseWait = 5 * time.Second
	waitLauncherFinish  = func(done <-chan struct{}) { <-done }
)

// SetLauncherReader overrides the launcher's CancelReader factory for a test.
func SetLauncherReader(fn func(io.Reader) (cancelreader.CancelReader, error)) func() {
	old := openLauncherReader
	openLauncherReader = fn
	return func() { openLauncherReader = old }
}

// SetLauncherReleaseWait overrides how long the launcher waits for a cancelled
// input read to exit before treating it as stuck.
func SetLauncherReleaseWait(d time.Duration) func() {
	old := launcherReleaseWait
	launcherReleaseWait = d
	return func() { launcherReleaseWait = old }
}

// SetLauncherFiles overrides the input and output terminal files for a test.
func SetLauncherFiles(in, out *os.File) func() {
	oldIn, oldOut := launcherIn, launcherOut
	launcherIn = func() *os.File { return in }
	launcherOut = func() *os.File { return out }
	return func() {
		launcherIn = oldIn
		launcherOut = oldOut
	}
}

type stuckLauncherError struct {
	*client.UserError
	cause error
}

func (e stuckLauncherError) Unwrap() []error {
	if e.cause != nil {
		return []error{e.UserError, client.ErrAttachReaderStuck, keyread.ErrStuck, e.cause}
	}
	return []error{e.UserError, client.ErrAttachReaderStuck, keyread.ErrStuck}
}

func stuckLauncherBusyError(cause error) error {
	return stuckLauncherError{
		UserError: &client.UserError{
			Msg:  "the terminal's reader from an earlier session is still running",
			Hint: brand.Name + " ls (lists the sessions in a new command)",
			Code: "terminal_busy",
		},
		cause: cause,
	}
}

func stuckLauncherAttachError(pa *pendingAttach) error {
	return stuckLauncherError{
		UserError: &client.UserError{
			Msg:  "the terminal's reader from the launcher is still running",
			Hint: brand.Name + " attach " + pa.host + "/" + pa.id + " (attaches in a new command)",
			Code: "terminal_busy",
		},
	}
}

// launcherInput owns the launcher's CancelReader on in. It satisfies Bubble
// Tea's term.File interface (Read, Write, Close, Fd) so Bubble Tea saves and
// restores raw mode on in, while omitting Name() so Bubble Tea's internal
// uv.NewCancelReader uses a pass-through reader rather than opening a second
// native reader on top of cr.
type launcherInput struct {
	in       *os.File
	original *term.State

	mu         sync.Mutex
	cr         cancelreader.CancelReader
	reading    bool
	stopping   bool
	finished   bool
	wasStuck   bool
	readDone   chan struct{}
	finishDone chan struct{}
}

func startLauncherInput(in *os.File) (*launcherInput, error) {
	if err := keyread.CheckStuck(in); err != nil {
		return nil, stuckLauncherBusyError(err)
	}
	original, err := launcherState(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("save the terminal mode: %w", err)
	}
	cr, err := openLauncherReader(in)
	if err != nil {
		if original != nil {
			_ = term.Restore(int(in.Fd()), original)
		}
		return nil, fmt.Errorf("read the terminal: %w", err)
	}
	return &launcherInput{in: in, original: original, cr: cr}, nil
}

func (li *launcherInput) Read(p []byte) (int, error) {
	li.mu.Lock()
	if li.stopping || li.cr == nil {
		li.mu.Unlock()
		return 0, cancelreader.ErrCanceled
	}
	cr := li.cr
	done := make(chan struct{})
	li.reading = true
	li.readDone = done
	li.mu.Unlock()

	defer func() {
		li.mu.Lock()
		li.reading = false
		close(done)
		li.mu.Unlock()
	}()
	return cr.Read(p) //nolint:wrapcheck // pass CancelReader errors directly to Bubble Tea
}

func (li *launcherInput) Write(p []byte) (int, error) {
	return li.in.Write(p) //nolint:wrapcheck // pass terminal writes directly
}

func (li *launcherInput) Close() error { return nil }

func (li *launcherInput) Fd() uintptr { return li.in.Fd() }

func (li *launcherInput) stopCurrent() bool {
	li.mu.Lock()
	li.stopping = true
	cr, reading, done := li.cr, li.reading, li.readDone
	li.cr = nil
	li.mu.Unlock()
	if cr == nil {
		return false
	}
	if !reading {
		_ = cr.Close()
		return false
	}
	var cause error
	if cr.Cancel() {
		select {
		case <-done:
			_ = cr.Close()
			return false
		case <-time.After(launcherReleaseWait):
			cause = keyread.ErrReadGoesOn
		}
	} else {
		select {
		case <-done:
			_ = cr.Close()
			return false
		default:
			cause = keyread.ErrCannotCancel
		}
	}
	keyread.HoldStuck(li.in, cr.Close, done, cause)
	return true
}

// finish joins the launcher's one stop and reports its final stuck result.
// Terminal restoration belongs to the outer runner after Program.Run, so a
// late handoff command cannot change the next terminal owner's mode.
func (li *launcherInput) finish() bool {
	li.mu.Lock()
	if li.finished {
		done := li.finishDone
		li.mu.Unlock()
		waitLauncherFinish(done)
		li.mu.Lock()
		stuck := li.wasStuck
		li.mu.Unlock()
		return stuck
	}
	li.finished = true
	li.finishDone = make(chan struct{})
	li.mu.Unlock()

	stuck := li.stopCurrent()
	li.mu.Lock()
	li.wasStuck = stuck
	close(li.finishDone)
	li.mu.Unlock()
	return stuck
}

func (li *launcherInput) pauseForExec() error {
	if li.stopCurrent() {
		return stuckLauncherBusyError(nil)
	}
	return nil
}

func (li *launcherInput) resumeAfterExec() error {
	if err := keyread.CheckStuck(li.in); err != nil {
		return stuckLauncherBusyError(err)
	}
	cr, err := openLauncherReader(li.in)
	if err != nil {
		return fmt.Errorf("read the terminal: %w", err)
	}
	li.mu.Lock()
	li.cr = cr
	li.stopping = false
	li.mu.Unlock()
	return nil
}

func (li *launcherInput) restoreTerminal() {
	if li.original != nil {
		_ = term.Restore(int(li.in.Fd()), li.original)
	}
}
