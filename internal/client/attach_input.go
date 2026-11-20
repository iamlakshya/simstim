package client

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// ErrAttachReaderStuck is wrapped when an earlier Attach on the same file
// could not stop its terminal reader and that read is still running.
var ErrAttachReaderStuck = errors.New("the terminal's reader from an earlier attach is still running")

type stuckReaderError struct {
	*UserError
	cause error
}

func (e stuckReaderError) Unwrap() []error {
	if e.cause != nil {
		return []error{e.UserError, ErrAttachReaderStuck, e.cause}
	}
	return []error{e.UserError, ErrAttachReaderStuck}
}

var (
	openAttachReader  = cancelreader.NewReader
	attachReleaseWait = 5 * time.Second
)

// SetAttachReader replaces the terminal reader factory used by Attach for a
// test and returns a function that restores the previous factory.
func SetAttachReader(f func(io.Reader) (cancelreader.CancelReader, error)) func() {
	old := openAttachReader
	openAttachReader = f
	return func() { openAttachReader = old }
}

// SetAttachReleaseWait changes how long Attach waits for a cancelled reader to
// exit in a test and returns a function that restores the previous duration.
func SetAttachReleaseWait(d time.Duration) func() {
	old := attachReleaseWait
	attachReleaseWait = d
	return func() { attachReleaseWait = old }
}

// checkAttachInput clears any finished reader from an earlier session on in,
// or fails when that earlier read is still running.
func checkAttachInput(in *os.File) error {
	if err := keyread.CheckStuck(in); err != nil {
		return stuckReaderError{
			UserError: &UserError{
				Msg:  "the terminal's reader from an earlier session is still running",
				Hint: "Run " + brand.Name + " attach again in a new command.",
				Code: "terminal_busy",
			},
			cause: err,
		}
	}
	return nil
}

type attachInput struct {
	in       *os.File
	cr       cancelreader.CancelReader
	wantRead chan struct{}
	stop     chan struct{}
	readDone chan struct{}

	mu       sync.Mutex
	reading  bool
	stopping bool
}

// startAttachInput opens the reader but leaves its first read parked until ask.
// The caller can enter raw mode after the factory saves the original state.
func startAttachInput(in *os.File, ch chan []byte, tr *attachTrace) (*attachInput, error) {
	if err := checkAttachInput(in); err != nil {
		return nil, err
	}
	cr, err := openAttachReader(in)
	if err != nil {
		return nil, fmt.Errorf("read the terminal: %w", err)
	}
	ai := &attachInput{
		in:       in,
		cr:       cr,
		wantRead: make(chan struct{}, 1),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	go func() {
		defer close(ai.readDone)
		buf := make([]byte, 32<<10)
		for {
			select {
			case <-ai.wantRead:
			case <-ai.stop:
				return
			}
			ai.mu.Lock()
			if ai.stopping {
				ai.mu.Unlock()
				return
			}
			ai.reading = true
			ai.mu.Unlock()

			n, err := cr.Read(buf)

			ai.mu.Lock()
			ai.reading = false
			ai.mu.Unlock()
			if n > 0 {
				tr.stdin(n)
				select {
				case ch <- append([]byte(nil), buf[:n]...):
				case <-ai.stop:
					return
				}
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	return ai, nil
}

func (ai *attachInput) ask() {
	if ai == nil {
		return
	}
	select {
	case ai.wantRead <- struct{}{}:
	default:
	}
}

// finish stops the reader before anything else reads or closes the terminal.
// When the read cannot be stopped, the reader stays open and recorded for
// ai.in rather than race with its close, and finish reports stuck = true.
func (ai *attachInput) finish() (stuck bool) {
	ai.mu.Lock()
	ai.stopping = true
	reading := ai.reading
	ai.mu.Unlock()
	close(ai.stop)
	if !reading {
		<-ai.readDone
		_ = ai.cr.Close()
		return false
	}
	var cause error
	if ai.cr.Cancel() {
		select {
		case <-ai.readDone:
			_ = ai.cr.Close()
			return false
		case <-time.After(attachReleaseWait):
			cause = keyread.ErrReadGoesOn
		}
	} else {
		select {
		case <-ai.readDone:
			_ = ai.cr.Close()
			return false
		default:
			cause = keyread.ErrCannotCancel
		}
	}
	keyread.HoldStuck(ai.in, ai.cr.Close, ai.readDone, cause)
	return true
}
