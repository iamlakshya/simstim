package keyread

import (
	"errors"
	"os"
)

// ErrStuck reports that an earlier reader on the same terminal file could not
// stop its read and that read is still waiting.
var ErrStuck = errors.New("the terminal's reader still waits in a read")

type stuckError struct{ cause error }

func (e stuckError) Error() string { return ErrStuck.Error() + ": " + e.cause.Error() }

func (e stuckError) Unwrap() []error { return []error{ErrStuck, e.cause} }

type heldReader struct {
	closeFn func() error
	done    <-chan struct{}
	cause   error
}

// heldReaders tracks non-keyread terminal readers (such as client.Attach, the
// launcher or the key tester) whose read could not be stopped when their owner
// ended. Guarded by readersMu.
var heldReaders = map[*os.File]heldReader{}

// HoldStuck records an abandoned reader on f whose read is still waiting until
// done closes. While done remains open, CheckStuck and For refuse to start
// another reader on f; once done closes, the next CheckStuck or Release closes
// the old reader via closeFn and clears the record.
func HoldStuck(f *os.File, closeFn func() error, done <-chan struct{}, cause error) {
	if f == nil {
		return
	}
	if cause == nil {
		cause = ErrCannotCancel
	}
	readersMu.Lock()
	heldReaders[f] = heldReader{closeFn: closeFn, done: done, cause: cause}
	readersMu.Unlock()
}

// CheckStuck clears any finished stuck reader on f and returns an error
// wrapping ErrStuck if an abandoned read is still waiting on f.
func CheckStuck(f *os.File) error {
	if f == nil {
		return nil
	}
	var toClose []func() error
	readersMu.Lock()
	if hr, ok := heldReaders[f]; ok {
		if hr.done == nil {
			readersMu.Unlock()
			return stuckError{cause: hr.cause}
		}
		select {
		case <-hr.done:
			delete(heldReaders, f)
			if hr.closeFn != nil {
				toClose = append(toClose, hr.closeFn)
			}
		default:
			readersMu.Unlock()
			return stuckError{cause: hr.cause}
		}
	}
	if r, ok := readers[f]; ok && r.stuck != nil {
		if r.stuckDone == nil {
			readersMu.Unlock()
			for _, fn := range toClose {
				_ = fn()
			}
			return stuckError{cause: r.stuck}
		}
		select {
		case <-r.stuckDone:
			delete(readers, f)
			toClose = append(toClose, r.src.Close)
		default:
			err := r.stuck
			readersMu.Unlock()
			for _, fn := range toClose {
				_ = fn()
			}
			return stuckError{cause: err}
		}
	}
	readersMu.Unlock()
	for _, fn := range toClose {
		_ = fn()
	}
	return nil
}
