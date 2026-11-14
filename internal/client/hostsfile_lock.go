package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The lock of hosts.toml. A command holds it only around one read, change
// and save of the file, so another one waits for it. The wait has an end:
// a holder may hang, as one stopped with Ctrl+Z does.

// The wait for the lock of hosts.toml: it tries every hostsPoll, says
// that it waits after hostsNote, and gives up after hostsWait. Tests make
// them short.
var (
	hostsPoll = 20 * time.Millisecond
	hostsNote = time.Second
	hostsWait = 10 * time.Second
)

// takeLock is lockFile. Tests make it fail, as a network disk without file
// locks does.
var takeLock = lockExclusive

// notesKey marks a context with the writer of its notes (WithNotes).
type notesKey struct{}

// WithNotes returns a copy of ctx whose notes, such as a wait for the lock
// of hosts.toml, go to w. With w nil, or without WithNotes, a wait says
// nothing: the launcher draws its own screen.
func WithNotes(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, notesKey{}, w)
}

// notesOf is the writer of ctx's notes, or nil.
func notesOf(ctx context.Context) io.Writer {
	w, _ := ctx.Value(notesKey{}).(io.Writer)
	return w
}

// ShortenHostsWait makes the wait for the lock of hosts.toml end after
// wait, for the tests of other packages. It returns the function that puts
// the old time back.
func ShortenHostsWait(wait time.Duration) func() {
	old := hostsWait
	hostsWait = wait
	return func() { hostsWait = old }
}

// lockHostsFile takes the lock of hosts.toml, and returns the function
// that lets it go. While another command holds it, it waits: after
// hostsNote it says so in the notes of ctx, and after hostsWait it fails
// with hosts_busy. It stops at once when ctx ends, as at Ctrl+C: a context
// that has ended gets no lock, also when the lock is free.
func lockHostsFile(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := hostsPath() + ".lock"
	f, err := openLock(path, "hosts.toml")
	if err != nil {
		return nil, err
	}
	start, noted := time.Now(), false
	for {
		err := takeLock(f)
		switch {
		case err == nil && ctx.Err() != nil:
			// The cancel came as the lock was taken.
			f.Close()
			return nil, ctx.Err()
		case err == nil:
			return func() { f.Close() }, nil
		case !errors.Is(err, errLockTaken):
			f.Close()
			return nil, flockFailed("hosts.toml", path, err)
		case time.Since(start) >= hostsWait:
			f.Close()
			return nil, &UserError{Msg: fmt.Sprintf("another command held %s for %s, so nothing was saved", hostsPath(), hostsWait),
				Hint: "End that command, such as one stopped with Ctrl+Z, then run this one again.", Code: "hosts_busy"}
		case !noted && time.Since(start) >= hostsNote:
			noted = true
			if w := notesOf(ctx); w != nil {
				fmt.Fprintf(w, "Waiting for another command that saves %s…\n", hostsPath())
			}
		}
		hostsFileWaiting()
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(hostsPoll):
		}
	}
}

// hostsFileWaiting lets inherited H3 tests observe a contended file lock.
var hostsFileWaiting = func() {}
