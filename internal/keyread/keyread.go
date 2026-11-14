// Package keyread reads keys from a terminal in raw mode for the client's
// questions and its picker (docs/contributing/cli-spec.md 6.2 and 6.3).
//
// It reads a byte at a time, and only when a caller asks for a key, so it
// never reads past the key that ends a question: what the user typed after
// it stays in the terminal for whatever reads next, such as an attach. A
// bracketed paste it reads in blocks, so that a big paste takes a moment.
// Its last block can hold keys typed within moments of the paste: they stay
// in the Reader for the next key, and a reader that is not this Reader,
// such as an attach, does not get them. A wait that runs out, such as the
// wait after a lone esc, never cancels the reader: the read stays for the
// next key. One Reader serves every question of a run, because a new
// reader on Windows flushes the console's input.
//
// A Reader is never cancelled and never closed while its terminal is in
// use. Its read may wait in its own goroutine at any time, after the end of
// a context too, and a reader closed under a running read would race with
// it. The read ends with the process, or takes the next key. Only Release
// ends a Reader, for a terminal that is about to close: it stops the read
// first, and leaves open a reader whose read it cannot stop.
package keyread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// Kind is what a key does.
type Kind int

// The keys that the questions and the picker tell apart. Other is any key
// that does nothing for them, such as an arrow to the side or a function
// key.
const (
	Other Kind = iota
	Rune
	Enter
	Esc
	CtrlC
	CtrlD
	Backspace
	CtrlU
	CtrlW
	CtrlP
	CtrlN
	Up
	Down
	// Paste is text that the terminal pasted between ESC [200~ and
	// ESC [201~. Its line breaks are text, never Enter.
	Paste
)

// Key is one key.
type Key struct {
	Kind Kind
	// Rune is the character of a Rune key.
	Rune rune
	// Text is the text of a Paste, as the terminal sent it.
	Text string
}

// EscWait is how long a lone esc waits for the rest of a key, such as an
// arrow, before it counts as esc.
const EscWait = 50 * time.Millisecond

// SeqWait is how long the rest of a sequence may take in all, once ESC [
// or ESC O has come: one deadline for every byte after the prefix. It is
// long, so that a slow terminal or a busy machine never cuts an arrow in
// two. It is not for ever, so that a stray prefix, such as alt+[ or an
// arrow whose end got lost, never eats the key after it: the "y" of a y/N
// question, say. After it the prefix is dropped, and the next key is
// itself.
const SeqWait = time.Second

// errWait is a wait that ran out. The read stays for the next call.
var errWait = errors.New("no byte came in time")

// Reader reads keys from one terminal.
type Reader struct {
	src cancelreader.CancelReader
	// res gets the byte of the read in flight, when it ends.
	res chan result
	// mu guards inFlight, reading, closed, want, readDone and back.
	mu       sync.Mutex
	inFlight bool
	reading  bool
	closed   bool
	// want asks the Reader's one read goroutine (reads) for a read of up
	// to n bytes, and readDone closes when that goroutine ends. Both are
	// nil while no such goroutine runs.
	want     chan int
	readDone chan struct{}
	// back holds bytes that a key read but did not use, for the next key.
	back []byte
	// after is time.After, and now time.Now. Tests replace both with a
	// clock of their own.
	after func(time.Duration) <-chan time.Time
	now   func() time.Time
	// file and fd are the terminal's, and first puts it back in the state
	// that it had before For made the reader. All three are unset for New.
	file  *os.File
	fd    int
	first func() error
	// stuck is why Release could not end the read of this Reader, and
	// stuckDone closes when that read goroutine exits. readersMu guards both.
	stuck     error
	stuckDone <-chan struct{}
}

// result is what a read gave: its bytes, or its error.
type result struct {
	data []byte
	err  error
}

var (
	readersMu sync.Mutex
	readers   = map[*os.File]*Reader{}
	// Open makes the reader of a file. On Windows it flushes the console's
	// input, and changes the console's modes, so For calls it once for each
	// file, after it saves the terminal's state. Tests replace it with a
	// reader of their own, also tests of a terminal's owner.
	Open = cancelreader.NewReader
	// saveState saves the state of the terminal fd, and returns what puts
	// it back. Tests replace it, and makeRaw, with a terminal of their own.
	saveState = func(fd int) (func() error, error) {
		st, err := term.GetState(fd)
		if err != nil {
			return nil, err //nolint:wrapcheck // For says what it was doing
		}
		return func() error { return term.Restore(fd, st) }, nil
	}
	// makeRaw puts the terminal fd in raw mode.
	makeRaw = func(fd int) error {
		_, err := term.MakeRaw(fd)
		return err //nolint:wrapcheck // Raw says what it was doing
	}
)

// For returns the Reader of f. It makes it at the first call, and returns
// the same Reader after that. Before it makes the reader, it saves the
// state of f's terminal, if f is one: on Windows, making the reader changes
// the console's modes, and Raw's restore must undo that too.
func For(f *os.File) (*Reader, error) {
	if err := CheckStuck(f); err != nil {
		return nil, fmt.Errorf("read the terminal: %w", err)
	}
	readersMu.Lock()
	defer readersMu.Unlock()
	if r, ok := readers[f]; ok {
		if r.stuck != nil {
			return nil, fmt.Errorf("read the terminal: %w", stuckError{cause: r.stuck})
		}
		return r, nil
	}
	fd := int(f.Fd())
	first, _ := saveState(fd)
	src, err := Open(f)
	if err != nil {
		// A reader that failed may have changed the console's modes first.
		if first != nil {
			_ = first()
		}
		return nil, fmt.Errorf("read the terminal: %w", err)
	}
	r := New(src)
	r.file, r.fd, r.first = f, fd, first
	readers[f] = r
	return r, nil
}

// ReleaseWait is how long Release waits for a cancelled read to end.
// Tests of a terminal's owner shorten it.
var ReleaseWait = 5 * time.Second

// The errors of a Release that could not end the read. With either, the
// read may still run, so the reader and its file must stay open.
var (
	// ErrCannotCancel is a read that its reader cannot cancel, as on some
	// Windows consoles.
	ErrCannotCancel = errors.New("the read of the terminal cannot be cancelled")
	// ErrReadGoesOn is a read that took its cancel, and did not end within
	// ReleaseWait. A terminal's reader that cancels does not do that: it is
	// a bug.
	ErrReadGoesOn = errors.New("the cancelled read of the terminal did not end")
)

// Release ends the Reader that For made for f, if any, before f closes, as
// a test's terminal does at its end. It cancels a read that waits, waits
// for it to end, and closes the reader. It returns nil once no read of f
// runs: only then may f close. When it cannot end the read, it returns
// ErrCannotCancel or ErrReadGoesOn, and keeps the Reader as stuck: a later
// Release returns the same error, and For no longer gives the Reader out.
// Release must come after the Reader's last ReadKey. The client never
// releases its own terminal: its read may wait until the process ends.
func Release(f *os.File) error {
	var closeHeld, closeStuck func() error
	readersMu.Lock()
	if hr, ok := heldReaders[f]; ok {
		if hr.done == nil {
			readersMu.Unlock()
			return hr.cause
		}
		select {
		case <-hr.done:
			delete(heldReaders, f)
			closeHeld = hr.closeFn
		default:
			readersMu.Unlock()
			return hr.cause
		}
	}
	r, ok := readers[f]
	if ok && r.stuck != nil {
		if r.stuckDone != nil {
			select {
			case <-r.stuckDone:
				delete(readers, f)
				closeStuck = r.src.Close
				ok = false
			default:
			}
		}
		if ok {
			err := r.stuck
			readersMu.Unlock()
			if closeHeld != nil {
				_ = closeHeld()
			}
			return err
		}
	}
	readersMu.Unlock()
	if closeHeld != nil {
		_ = closeHeld()
	}
	if closeStuck != nil {
		_ = closeStuck()
	}
	if !ok {
		return nil
	}
	if done, err := r.end(); err != nil {
		readersMu.Lock()
		r.stuck, r.stuckDone = err, done
		readersMu.Unlock()
		return err
	}
	readersMu.Lock()
	delete(readers, f)
	readersMu.Unlock()
	return nil
}

// Raw puts the terminal of r in raw mode, and returns what restores it:
// back to the state that For saved before it made the reader, whatever the
// reader or the raw mode changed since. The restore runs once, however
// often it is called, so a caller defers it, for a panic, and calls it too
// where its order matters. Raw fails for a Reader that is not of a
// terminal.
func (r *Reader) Raw() (func() error, error) {
	if r.file != nil {
		if err := CheckStuck(r.file); err != nil {
			return nil, fmt.Errorf("put the terminal in raw mode: %w", err)
		}
	}
	if r.first == nil {
		return nil, errors.New("put the terminal in raw mode: it is not a terminal")
	}
	if err := makeRaw(r.fd); err != nil {
		_ = r.first()
		return nil, fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() { err = r.first() })
		return err
	}, nil
}

// New returns a Reader of src.
func New(src cancelreader.CancelReader) *Reader {
	return &Reader{src: src, res: make(chan result, 1), after: time.After, now: time.Now}
}

// next returns the next byte. With a wait above 0 it gives up after wait
// with errWait, and the read stays in flight for the next call. The end of
// ctx leaves it in flight too.
func (r *Reader) next(ctx context.Context, wait time.Duration) (byte, error) {
	if r.file != nil {
		if err := CheckStuck(r.file); err != nil {
			return 0, fmt.Errorf("read the terminal: %w", err)
		}
	}
	r.mu.Lock()
	if len(r.back) > 0 {
		b := r.back[0]
		r.back = r.back[1:]
		r.mu.Unlock()
		return b, nil
	}
	if r.closed {
		r.mu.Unlock()
		return 0, fmt.Errorf("read the terminal: %w", cancelreader.ErrCanceled)
	}
	// The wait starts before the read, so that a read that has started
	// always has its wait.
	var timeout <-chan time.Time
	if wait > 0 {
		timeout = r.after(wait)
	}
	r.start(1)
	r.mu.Unlock()
	select {
	case res := <-r.res:
		data, err := r.took(res)
		if err != nil {
			return 0, err
		}
		r.unread(data[1:]...)
		return data[0], nil
	case <-timeout:
		return 0, errWait
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// unread gives b back, before anything left already, for the next key.
func (r *Reader) unread(b ...byte) {
	if len(b) == 0 {
		return
	}
	r.mu.Lock()
	r.back = append(append([]byte(nil), b...), r.back...)
	r.mu.Unlock()
}

// control is the key of a control byte, and whether b is one. Enter,
// ctrl+c and ctrl+d end anything that is not whole yet, such as half a
// rune or half an escape sequence. In a paste, only ctrl+c does (paste).
func control(b byte) (Key, bool) {
	switch b {
	case '\r', '\n':
		return Key{Kind: Enter}, true
	case 0x03:
		return Key{Kind: CtrlC}, true
	case 0x04:
		return Key{Kind: CtrlD}, true
	case 0x7f, 0x08:
		return Key{Kind: Backspace}, true
	case 0x15:
		return Key{Kind: CtrlU}, true
	case 0x17:
		return Key{Kind: CtrlW}, true
	case 0x10:
		return Key{Kind: CtrlP}, true
	case 0x0e:
		return Key{Kind: CtrlN}, true
	}
	if b < 0x20 && b != 0x1b {
		return Key{Kind: Other}, true
	}
	return Key{}, false
}

// ReadKey reads one key. A lone esc counts as esc after EscWait. A key of
// more than one byte that a control byte cuts short is dropped, and the
// control byte is the key.
func (r *Reader) ReadKey(ctx context.Context) (Key, error) {
	b, err := r.next(ctx, 0)
	if err != nil {
		return Key{}, err
	}
	if k, ok := control(b); ok {
		return k, nil
	}
	switch {
	case b == 0x1b:
		return r.escape(ctx)
	case b < utf8.RuneSelf:
		return Key{Kind: Rune, Rune: rune(b)}, nil
	}
	return r.multibyte(ctx, b)
}

// escape reads the rest of a key that starts with esc. The byte after the
// esc has a wait, EscWait: with none, the key is esc itself. Once the esc
// has a CSI or SS3 prefix after it, the rest of the sequence has SeqWait in
// all: within it the sequence stays whole, and at its end the prefix is
// dropped. ESC [200~ starts a paste, which runs to ESC [201~ (bracketed
// paste).
func (r *Reader) escape(ctx context.Context) (Key, error) {
	b, err := r.next(ctx, EscWait)
	switch {
	case errors.Is(err, errWait):
		return Key{Kind: Esc}, nil
	case err != nil:
		return Key{}, err
	}
	if k, ok := control(b); ok {
		return k, nil
	}
	switch b {
	case 0x1b:
		r.unread(b)
		return Key{Kind: Esc}, nil
	case '[', 'O':
	default:
		return Key{Kind: Other}, nil // esc and a key: the key with alt
	}
	intro := b
	var params []byte
	deadline := r.now().Add(SeqWait)
	for range 32 {
		wait := deadline.Sub(r.now())
		if wait <= 0 {
			return Key{Kind: Other}, nil // a stray prefix: dropped
		}
		c, err := r.next(ctx, wait)
		switch {
		case errors.Is(err, errWait):
			return Key{Kind: Other}, nil // a stray prefix: dropped
		case err != nil:
			return Key{}, err
		}
		if k, ok := control(c); ok {
			return k, nil
		}
		if c == 0x1b {
			r.unread(c)
			return Key{Kind: Other}, nil
		}
		if c >= 0x40 && c <= 0x7e {
			switch {
			case intro == '[' && c == '~' && string(params) == "200":
				return r.paste(ctx)
			case c == 'A':
				return Key{Kind: Up}, nil
			case c == 'B':
				return Key{Kind: Down}, nil
			}
			return Key{Kind: Other}, nil
		}
		params = append(params, c)
	}
	return Key{Kind: Other}, nil
}

// multibyte reads the rest of a rune whose first byte is first. A byte that
// cannot go on the rune ends it: the rune is dropped, and the byte is read
// again as the start of the next key.
func (r *Reader) multibyte(ctx context.Context, first byte) (Key, error) {
	buf := []byte{first}
	for !utf8.FullRune(buf) {
		b, err := r.next(ctx, 0)
		if err != nil {
			return Key{}, err
		}
		if k, ok := control(b); ok {
			return k, nil
		}
		if b < 0x80 || b > 0xbf {
			r.unread(b)
			return Key{Kind: Other}, nil
		}
		buf = append(buf, b)
	}
	c, _ := utf8.DecodeRune(buf)
	if c == utf8.RuneError || !unicode.IsPrint(c) {
		return Key{Kind: Other}, nil
	}
	return Key{Kind: Rune, Rune: c}, nil
}
