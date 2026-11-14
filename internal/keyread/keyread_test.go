package keyread

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/muesli/cancelreader"
)

// fake is a terminal that gives the bytes a test writes, one Read at a
// time.
type fake struct {
	bytes chan byte
	// entered, if set, gets a value as each Read starts.
	entered  chan struct{}
	cancel   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	reads    int
	canceled bool
	closed   bool
}

func newFake() *fake { return &fake{bytes: make(chan byte, 64), cancel: make(chan struct{})} }

func (f *fake) write(s string) {
	for i := range len(s) {
		f.bytes <- s[i]
	}
}

func (f *fake) Read(p []byte) (int, error) {
	f.mu.Lock()
	f.reads++
	entered := f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	select {
	case b := <-f.bytes:
		// One byte, and then what else has come, as a terminal gives it.
		p[0] = b
		n := 1
		for n < len(p) {
			select {
			case b := <-f.bytes:
				p[n] = b
				n++
				continue
			default:
			}
			break
		}
		return n, nil
	case <-f.cancel:
		return 0, cancelreader.ErrCanceled
	}
}

func (f *fake) Cancel() bool {
	f.once.Do(func() { close(f.cancel) })
	f.mu.Lock()
	f.canceled = true
	f.mu.Unlock()
	return true
}

func (f *fake) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

// touched reports whether the fake was cancelled or closed.
func (f *fake) touched() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.canceled || f.closed
}

// keys reads n keys.
func keys(t *testing.T, r *Reader, n int) []Key {
	t.Helper()
	var out []Key
	for range n {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		k, err := r.ReadKey(ctx)
		cancel()
		if err != nil {
			t.Fatalf("after %v: %v", out, err)
		}
		out = append(out, k)
	}
	return out
}

func TestReadKey(t *testing.T) {
	src := newFake()
	r := New(src)
	src.write("aé𝄞\x1b[A\x1bOB\x1b[1;5C\r\x03\x04\x7f\x15\x17\x10\x0e\x01")
	want := []Key{{Kind: Rune, Rune: 'a'}, {Kind: Rune, Rune: 'é'}, {Kind: Rune, Rune: '𝄞'}, {Kind: Up}, {Kind: Down}, {Kind: Other}, {Kind: Enter}, {Kind: CtrlC},
		{Kind: CtrlD}, {Kind: Backspace}, {Kind: CtrlU}, {Kind: CtrlW}, {Kind: CtrlP}, {Kind: CtrlN}, {Kind: Other}}
	got := keys(t, r, len(want))
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Enter, ctrl+c and ctrl+d end half a rune or half an escape sequence: the
// half is dropped, and the control is the key. A byte that cannot go on a
// rune starts the next key.
func TestControlsEndAPartialKey(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []Key
	}{
		{"\xf0\x03", []Key{{Kind: CtrlC}}},
		{"\x1b[\x03", []Key{{Kind: CtrlC}}},
		{"\x1b[1;\r", []Key{{Kind: Enter}}},
		{"\xc3\r", []Key{{Kind: Enter}}},
		{"\xe2\x82\x04", []Key{{Kind: CtrlD}}},
		{"\xc3a", []Key{{Kind: Other}, {Kind: Rune, Rune: 'a'}}},
		{"\x1b\x03", []Key{{Kind: CtrlC}}},
		{"\x1b\x1b[A", []Key{{Kind: Esc}, {Kind: Up}}},
	} {
		src := newFake()
		r := New(src)
		src.write(c.in)
		got := keys(t, r, len(c.want))
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%q: key %d = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// A clock of the test's own, for the wait after esc. Only the first wait
// after arm can run out: the waits after it never do.
type clock struct {
	fire  chan time.Time
	armed bool
}

func (c *clock) arm() { c.armed = true; c.fire <- time.Now() }

func (c *clock) after(time.Duration) <-chan time.Time {
	if !c.armed {
		return nil
	}
	c.armed = false
	return c.fire
}

// A lone esc is esc when its wait runs out. The wait never cancels the
// reader: a byte that comes at the deadline, or after it, is read by the
// next key, and nothing fails.
func TestEscWaitNeverCancelsTheReader(t *testing.T) {
	src := newFake()
	r := New(src)
	clk := &clock{fire: make(chan time.Time, 1)}
	r.after = clk.after
	src.write("\x1b")
	clk.arm()
	if k := keys(t, r, 1); k[0].Kind != Esc {
		t.Fatalf("a lone esc = %+v", k[0])
	}
	src.write("[A")
	if k := keys(t, r, 2); k[0] != (Key{Kind: Rune, Rune: '['}) || k[1] != (Key{Kind: Rune, Rune: 'A'}) {
		t.Fatalf("after the wait: %+v", k)
	}
	// The byte and the deadline at once: either the wait or the byte wins,
	// and every byte is read in order, with no error.
	for range 50 {
		src.write("\x1b[B")
		clk.arm()
		k := keys(t, r, 1)
		switch k[0].Kind {
		case Down:
		case Esc:
			if rest := keys(t, r, 2); rest[0] != (Key{Kind: Rune, Rune: '['}) || rest[1] != (Key{Kind: Rune, Rune: 'B'}) {
				t.Fatalf("after esc: %+v", rest)
			}
		default:
			t.Fatalf("esc [ B at the deadline = %+v", k)
		}
		select {
		case <-clk.fire:
		default:
		}
	}
	if src.canceled {
		t.Error("a wait cancelled the reader")
	}
}

// A key read never reads past itself: two answers in one write are two
// keys apart, and the end of ctx leaves the read for the next call.
func TestReadsOnlyWhatItNeeds(t *testing.T) {
	src := newFake()
	r := New(src)
	src.write("y\rn\r")
	if k := keys(t, r, 2); k[1].Kind != Enter {
		t.Fatalf("first answer: %+v", k)
	}
	if n := len(src.bytes); n != 2 {
		t.Fatalf("%d bytes left after the first answer, want 2", n)
	}
	if k := keys(t, r, 2); k[0] != (Key{Kind: Rune, Rune: 'n'}) || k[1].Kind != Enter {
		t.Fatalf("second answer: %+v", k)
	}
	// Nothing is left: once the read waits, the context ends. The read
	// stays, and the end of the context neither cancels nor closes the
	// reader under it.
	src.mu.Lock()
	src.entered = make(chan struct{}, 1)
	src.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-src.entered
		cancel()
	}()
	if _, err := r.ReadKey(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a context that ends while the read waits: %v", err)
	}
	if src.touched() {
		t.Fatal("the end of a context cancelled or closed the reader")
	}
}

// For makes one reader for a file, however many questions read it: on
// Windows a new reader flushes the console's input, and the second answer
// of one write would be lost.
func TestForMakesOneReaderForEachFile(t *testing.T) {
	src := newFake()
	opens := 0
	old := Open
	// Like the Windows reader, a new reader flushes what waits in the
	// console.
	Open = func(io.Reader) (cancelreader.CancelReader, error) {
		opens++
		for len(src.bytes) > 0 {
			<-src.bytes
		}
		return src, nil
	}
	defer func() { Open = old }()
	f := tempFile(t)
	r, err := For(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = r
	src.write("y\rn\r")
	for _, want := range []rune{'y', 'n'} {
		r, err := For(f)
		if err != nil {
			t.Fatal(err)
		}
		if k := keys(t, r, 2); k[0] != (Key{Kind: Rune, Rune: want}) || k[1].Kind != Enter {
			t.Fatalf("answer %c: %+v", want, k)
		}
	}
	if opens != 1 {
		t.Errorf("made %d readers for one file, want 1", opens)
	}
}

// The first For of a terminal saves its state before it makes the reader,
// which on Windows changes the console's modes (prepareConsole). After each
// prompt, the restore puts back that first state: what the reader and the
// raw mode changed is undone.
func TestRestoreGoesBackToTheFirstState(t *testing.T) {
	src := newFake()
	mode := "cooked"
	oldOpen, oldSave, oldRaw := Open, saveState, makeRaw
	defer func() { Open, saveState, makeRaw = oldOpen, oldSave, oldRaw }()
	saveState = func(int) (func() error, error) {
		saved := mode
		return func() error { mode = saved; return nil }, nil
	}
	Open = func(io.Reader) (cancelreader.CancelReader, error) {
		mode = "changed by the reader"
		return src, nil
	}
	makeRaw = func(int) error { mode = "raw"; return nil }
	f := tempFile(t)
	for _, answer := range []string{"y\r", "n\r"} {
		r, err := For(f)
		if err != nil {
			t.Fatal(err)
		}
		restore, err := r.Raw()
		if err != nil || mode != "raw" {
			t.Fatalf("Raw: %v, the terminal is %s", err, mode)
		}
		src.write(answer)
		keys(t, r, 2)
		if err := restore(); err != nil {
			t.Fatal(err)
		}
		if mode != "cooked" {
			t.Errorf("after the prompt %q the terminal is %s, want cooked", answer, mode)
		}
	}
	// A reader of what is not a terminal has no raw mode.
	if _, err := New(src).Raw(); err == nil {
		t.Error("Raw of a reader that is not of a terminal did not fail")
	}
}

// tempFile is a file for For, in a folder of the test. Its Reader is
// released, and it is closed, before the folder goes: Windows cannot remove
// a file that is open.
func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A read that Release could not stop may still use f: then f stays
		// open, and the test leaks it rather than close it under the read.
		if Release(f) == nil {
			_ = f.Close()
		}
	})
	return f
}

// unstick forgets the stuck Reader of f, once the test has made sure that
// its read has ended, so that f may close.
func unstick(f *os.File) {
	readersMu.Lock()
	defer readersMu.Unlock()
	delete(readers, f)
	delete(heldReaders, f)
}

// Release stops a read that waits, and returns, and closes the reader,
// only once the read has ended. The read is held: in a bubble of its own
// (testing/synctest), Release must still wait when every goroutine is
// blocked, and it ends, with the reader closed, only after the test lets
// the read go. Then For forgets the file. A read that cannot be stopped
// keeps its reader open and stuck: Release says so at once, and again at a
// second call, and For makes no second reader of the file.
func TestReleaseStopsTheReadFirst(t *testing.T) {
	for _, cancels := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			f := tempFile(t)
			src := newHeld(cancels)
			opens := 0
			old := Open
			Open = func(io.Reader) (cancelreader.CancelReader, error) { opens++; return src, nil }
			defer func() { Open = old }()
			r, err := For(f)
			if err != nil {
				t.Fatal(err)
			}
			var releaseDone chan struct{}
			t.Cleanup(func() {
				// Capture the worker before letting its held Read return.
				r.mu.Lock()
				readDone := r.readDone
				r.mu.Unlock()
				src.letGo()
				if releaseDone != nil {
					<-releaseDone
				}
				if readDone != nil {
					<-readDone
				}
				// Release can move the worker's channel into its stuck record.
				readersMu.Lock()
				stuckDone := r.stuckDone
				readersMu.Unlock()
				if stuckDone != nil {
					<-stuckDone
				}
				unstick(f) // Release and its physical Read have both ended.
			})
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				<-src.entered
				cancel()
			}()
			if _, err := r.ReadKey(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("the read did not wait: %v", err)
			}
			var returned atomic.Bool
			var got error
			releaseDone = make(chan struct{})
			go func() {
				defer close(releaseDone)
				got = Release(f)
				returned.Store(true)
			}()
			synctest.Wait() // every goroutine waits: the read is held
			src.mu.Lock()
			closed := src.closed
			src.mu.Unlock()
			if cancels && (returned.Load() || closed) {
				t.Fatalf("Release returned %v, and closed the reader %v, while the read was held", returned.Load(), closed)
			}
			if !cancels {
				if !returned.Load() || !errors.Is(got, ErrCannotCancel) || closed {
					t.Fatalf("a read that cannot be stopped: Release returned %v, with %v, closed %v", returned.Load(), got, closed)
				}
				if err := Release(f); !errors.Is(err, ErrCannotCancel) {
					t.Errorf("a second Release of a stuck file: %v", err)
				}
				if _, err := For(f); err == nil || opens != 1 {
					t.Errorf("For of a stuck file: %v, and %d readers made", err, opens)
				}
			}
			src.letGo()
			synctest.Wait()
			src.mu.Lock()
			closed, under := src.closed, src.closedWhileReading
			src.mu.Unlock()
			if under || closed != cancels || cancels && got != nil {
				t.Errorf("cancel works %v: Release returned %v, closed %v, under the read %v", cancels, got, closed, under)
			}
			readersMu.Lock()
			_, kept := readers[f]
			readersMu.Unlock()
			if kept != !cancels {
				t.Errorf("cancel works %v: For kept the file %v", cancels, kept)
			}
		})
	}
}

// held is a terminal's reader whose read starts, says so, and waits until
// the test lets it go, whatever Cancel does. Cancel works or fails, as on
// some Windows consoles. Close notes, for good, a close during a read.
type held struct {
	cancels bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	reading bool
	closed  bool
	// closedWhileReading stays true once a Close came during a Read.
	closedWhileReading bool
}

func newHeld(cancels bool) *held {
	return &held{cancels: cancels, entered: make(chan struct{}, 1), release: make(chan struct{})}
}

// A bracketed paste is one key, Paste, with its text whole: a line break,
// ctrl+c and esc in it are text. It may come in many reads, however late.
// What looks like the start of its end but is not stays text.
func TestAPasteIsOneKey(t *testing.T) {
	src := newFake()
	src.entered = make(chan struct{}, 64)
	r := New(src)
	got := make(chan Key, 1)
	// The first part is there before the read starts: six reads of a byte
	// for ESC [200~, one block for "a\nre", and a block read that waits.
	src.write("\x1b[200~a\nre")
	go func() {
		k, err := r.ReadKey(context.Background())
		if err != nil {
			t.Error(err)
		}
		got <- k
	}()
	if !started(src, 8) {
		t.Fatalf("the read after the first part of the paste never started: %+v", first(t, got, "a paste"))
	}
	src.write("st\x1b[20x\r\x1b[201~y")
	if k := first(t, got, "a paste"); k.Kind != Paste || k.Text != "a\nrest\x1b[20x\r" {
		t.Fatalf("a paste: %+v", k)
	}
	if k := keys(t, r, 1); k[0] != (Key{Kind: Rune, Rune: 'y'}) {
		t.Errorf("the key after the paste: %+v", k[0])
	}
}

// A paste whose end never comes has a way out: ctrl+c in it is the key
// ctrl+c, a cancel, and what comes after it is the next key.
func TestCtrlCEndsAPaste(t *testing.T) {
	src := newFake()
	r := New(src)
	src.write("\x1b[200~half a paste\x03y")
	if k := keys(t, r, 2); k[0].Kind != CtrlC || k[1] != (Key{Kind: Rune, Rune: 'y'}) {
		t.Errorf("ESC [200~, text, ctrl+c and y: %+v", k)
	}
}

// A paste keeps MaxPaste bytes, reads the rest to its end, and the key
// after it is itself.
func TestAPasteIsCapped(t *testing.T) {
	src := newFake()
	r := New(src)
	go func() {
		src.write("\x1b[200~")
		src.write(strings.Repeat("x", MaxPaste+100))
		src.write("\x1b[201~y")
	}()
	if k := keys(t, r, 2); k[0].Kind != Paste || len(k[0].Text) != MaxPaste || k[1] != (Key{Kind: Rune, Rune: 'y'}) {
		t.Errorf("a paste of %d bytes: %v, %d bytes kept, then %+v", MaxPaste+100, k[0].Kind, len(k[0].Text), k[1])
	}
}

// OneLine keeps what a typed key gives: no line break, U+2028 and U+2029
// too, and no control or hidden character.
func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"a\nrest\x03\r\tz":      "arestz",
		"ab\u202ecd":            "abcd", // right-to-left override
		"ab\u200bcd":            "abcd", // zero-width space
		"ab\ufeffcd":            "abcd", // byte order mark
		"ab\u2028cd\u2029ef":    "abcdef",
		"ab\U000E0041cd":        "abcd", // tag A
		"ab\u00adcd":            "abcd", // soft hyphen
		"\x1b[31mred\u0085line": "[31mredline",
		"é 世 𝄞":                 "é 世 𝄞",
		"a\ufffdb":              "ab",  // a pasted replacement character
		"a\xffb":                "ab",  // a byte that is no rune
		"xyz\xc3":               "xyz", // a rune that MaxPaste cut
	} {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// A key cut in two reads is one key, wherever the cut: a rune, and a
// sequence after the esc or after its CSI or SS3 prefix. The test's clock
// never runs out: the rest comes within each wait. A rest that comes late
// is TestADelayedSequenceStaysWhole's.
func TestASplitKeyIsOneKey(t *testing.T) {
	for _, c := range []struct {
		key  string
		want Key
	}{
		{"\x1b[1;5C", Key{Kind: Other}},
		{"\x1bOA", Key{Kind: Up}},
		{"\x1b[A", Key{Kind: Up}},
		{"𝄞", Key{Kind: Rune, Rune: '𝄞'}},
		{"é", Key{Kind: Rune, Rune: 'é'}},
	} {
		for cut := 1; cut < len(c.key); cut++ {
			src := newFake()
			src.entered = make(chan struct{}, 16)
			r := New(src)
			r.after = func(time.Duration) <-chan time.Time { return nil }
			r.now = newTimeline().now // time stands still
			got := make(chan Key, 1)
			go func() {
				k, err := r.ReadKey(context.Background())
				if err != nil {
					t.Error(err)
				}
				got <- k
			}()
			src.write(c.key[:cut])
			// The read after the first part has started, and waits: unless
			// the key has ended early.
			if !started(src, cut+1) {
				t.Errorf("%q cut at %d: the key ended before the rest came: %+v", c.key, cut, <-got)
				continue
			}
			src.write(c.key[cut:] + "y")
			if k := <-got; k != c.want {
				t.Errorf("%q cut at %d: %+v, want %+v", c.key, cut, k, c.want)
			}
			if k := keys(t, r, 1); k[0] != (Key{Kind: Rune, Rune: 'y'}) {
				t.Errorf("%q cut at %d: the key after it is %+v", c.key, cut, k[0])
			}
		}
	}
}

func (h *held) letGo() { h.once.Do(func() { close(h.release) }) }

func (h *held) Read([]byte) (int, error) {
	h.mu.Lock()
	h.reading = true
	h.mu.Unlock()
	h.entered <- struct{}{}
	<-h.release
	h.mu.Lock()
	h.reading = false
	h.mu.Unlock()
	return 0, cancelreader.ErrCanceled
}

func (h *held) Cancel() bool { return h.cancels }

func (h *held) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.reading {
		h.closedWhileReading = true
	}
	return nil
}

// started waits until n reads of src have started, and reports whether
// they did within five seconds.
func started(src *fake, n int) bool {
	for range n {
		select {
		case <-src.entered:
		case <-time.After(5 * time.Second):
			return false
		}
	}
	return true
}

// timeline is a clock of the test's own. Its time moves only when the test
// says: set moves it, and fires every wait whose end has come.
type timeline struct {
	mu    sync.Mutex
	start time.Time
	at    time.Time
	waits []pending
}

type pending struct {
	end time.Time
	ch  chan time.Time
}

func newTimeline() *timeline {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	return &timeline{start: t0, at: t0}
}

func (c *timeline) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *timeline) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waits = append(c.waits, pending{c.at.Add(d), ch})
	c.mu.Unlock()
	return ch
}

// set moves the clock to d after its start.
func (c *timeline) set(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.start.Add(d)
	var left []pending
	for _, w := range c.waits {
		if !w.end.After(c.at) {
			w.ch <- c.at
		} else {
			left = append(left, w)
		}
	}
	c.waits = left
}

// onTimeline is a reader of a fake terminal, on a clock of the test's own,
// whose ReadKey runs in the background.
func onTimeline(t *testing.T) (*fake, *Reader, *timeline, func() chan Key) {
	t.Helper()
	src := newFake()
	src.entered = make(chan struct{}, 16)
	r := New(src)
	clk := newTimeline()
	r.after, r.now = clk.after, clk.now
	read := func() chan Key {
		got := make(chan Key, 1)
		go func() {
			k, err := r.ReadKey(context.Background())
			if err != nil {
				t.Error(err)
			}
			got <- k
		}()
		return got
	}
	return src, r, clk, read
}

// first is the key that got gives within five seconds.
func first(t *testing.T, got chan Key, what string) Key {
	t.Helper()
	select {
	case k := <-got:
		return k
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no key came", what)
		return Key{}
	}
}

// tracer is a terminal that gives one byte a read, and notes the
// goroutine and the buffer of each read.
type tracer struct {
	bytes      chan byte
	mu         sync.Mutex
	goroutines map[string]bool
	buffers    map[*byte]bool
}

func (s *tracer) Read(p []byte) (int, error) {
	b := <-s.bytes
	stack := make([]byte, 64)
	stack = stack[:runtime.Stack(stack, false)] // "goroutine 7 [running]: ..."
	s.mu.Lock()
	s.goroutines[strings.Fields(string(stack))[1]] = true
	s.buffers[&p[0]] = true
	s.mu.Unlock()
	p[0] = b
	return 1, nil
}

func (*tracer) Cancel() bool { return false }
func (*tracer) Close() error { return nil }

// One goroutine of the Reader makes every read, into one buffer: the
// reads of keys a byte at a time, and the reads of a paste in blocks. A
// paste of a thousand reads starts no thousand goroutines, and makes no
// thousand buffers.
func TestOneGoroutineAndOneBufferReadEverything(t *testing.T) {
	src := &tracer{bytes: make(chan byte, 256), goroutines: map[string]bool{}, buffers: map[*byte]bool{}}
	r := New(src)
	keys := strings.Repeat("ab", 20) + "\x1b[200~a paste of a byte a read\x1b[201~"
	for _, b := range []byte(keys) {
		src.bytes <- b
	}
	for range 41 {
		if _, err := r.ReadKey(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.goroutines) != 1 || len(src.buffers) != 1 {
		t.Errorf("%d reads came from %d goroutines, into %d buffers, want one of each", len(keys), len(src.goroutines), len(src.buffers))
	}
}

// clocked is a terminal that gives one byte a read, and moves the clock
// past the sequence's deadline as it gives jump.
type clocked struct {
	bytes chan byte
	jump  byte
	now   atomic.Int64
}

func (c *clocked) Read(p []byte) (int, error) {
	b := <-c.bytes
	if b == c.jump {
		c.now.Add(int64(2 * SeqWait))
	}
	p[0] = b
	return 1, nil
}

func (*clocked) Cancel() bool { return false }
func (*clocked) Close() error { return nil }

// A sequence whose deadline passes while one of its bytes comes ends at
// once, with that byte: the next byte is a key of its own, the "y" of a
// y/N question, and not the end of the sequence. The waits never run out:
// only the clock that the deadline reads moves.
func TestASequencePastItsDeadlineEndsAtOnce(t *testing.T) {
	src := &clocked{bytes: make(chan byte, 16), jump: '1'}
	r := New(src)
	start := time.Unix(1000, 0)
	r.now = func() time.Time { return start.Add(time.Duration(src.now.Load())) }
	r.after = func(time.Duration) <-chan time.Time { return nil }
	got := make(chan Key, 4)
	go func() {
		for range 2 {
			k, err := r.ReadKey(context.Background())
			if err != nil {
				return
			}
			got <- k
		}
	}()
	for _, b := range []byte("\x1b[1y") {
		src.bytes <- b
	}
	if k := first(t, got, "ESC [ 1, then the deadline"); k.Kind != Other {
		t.Errorf("ESC [ 1 past its deadline: %+v", k)
	}
	if k := first(t, got, "the key after it"); k != (Key{Kind: Rune, Rune: 'y'}) {
		t.Errorf("the key after a sequence past its deadline: %+v", k)
	}
}

// Once ESC [ has come, the rest of the sequence has SeqWait in all, a
// second, on one deadline. A at 0.1 s, after the esc wait would have run
// out, and at 0.9 s gives up, not esc and "[A". At 1.1 s the prefix has
// been dropped at 1 s, and the keys after it are themselves: "y" and
// Enter, as at a y/N question. A rest whose bytes come at 0.4 s, 0.8 s and
// 1.2 s is dropped at 1 s too: no byte gets a second of its own. A bare
// esc is esc once its own wait runs out.
func TestADelayedSequenceStaysWhole(t *testing.T) {
	for _, c := range []struct {
		at time.Duration
		up bool
	}{
		{100 * time.Millisecond, true},
		{900 * time.Millisecond, true},
		{1100 * time.Millisecond, false},
	} {
		src, r, clk, read := onTimeline(t)
		src.write("\x1b[")
		got := read()
		if !started(src, 3) {
			t.Fatalf("%s: the read after [ never started", c.at)
		}
		clk.set(c.at)
		if c.up {
			src.write("A")
			if k := first(t, got, c.at.String()); k.Kind != Up {
				t.Errorf("ESC [, then A at %s: %+v, want up", c.at, k)
			}
			continue
		}
		if k := first(t, got, c.at.String()); k.Kind != Other {
			t.Fatalf("ESC [, then nothing until %s: %+v, want the prefix dropped", c.at, k)
		}
		src.write("y\r")
		if k := keys(t, r, 2); k[0] != (Key{Kind: Rune, Rune: 'y'}) || k[1].Kind != Enter {
			t.Errorf("y and Enter after a dropped prefix: %+v", k)
		}
	}

	src, r, clk, read := onTimeline(t)
	src.write("\x1b[")
	got := read()
	for i, part := range []struct {
		at   time.Duration
		byte string
	}{{400 * time.Millisecond, "1"}, {800 * time.Millisecond, ";"}} {
		// ESC and [ have been read, and then each byte before this one.
		if !started(src, []int{3, 1}[i]) {
			t.Fatalf("the read before %q never started", part.byte)
		}
		clk.set(part.at)
		src.write(part.byte)
	}
	if !started(src, 1) {
		t.Fatal("the read after ; never started")
	}
	clk.set(1200 * time.Millisecond)
	if k := first(t, got, "a rest across the deadline"); k.Kind != Other {
		t.Fatalf("ESC [ 1 ; and then nothing until 1.2 s: %+v, want the prefix dropped", k)
	}
	src.write("y\r")
	if k := keys(t, r, 2); k[0] != (Key{Kind: Rune, Rune: 'y'}) || k[1].Kind != Enter {
		t.Errorf("y and Enter after a rest across the deadline: %+v", k)
	}

	src, _, clk, read = onTimeline(t)
	src.write("\x1b")
	got = read()
	if !started(src, 2) {
		t.Fatal("the read after the esc never started")
	}
	clk.set(EscWait)
	if k := first(t, got, "a bare esc"); k.Kind != Esc {
		t.Fatalf("a bare esc: the key is %+v, want esc", k)
	}
}

// A reader that fails after it changed the terminal's modes, as Windows'
// can after prepareConsole, leaves the terminal as it was.
func TestAFailedOpenRestoresTheFirstState(t *testing.T) {
	mode := "cooked"
	oldOpen, oldSave := Open, saveState
	defer func() { Open, saveState = oldOpen, oldSave }()
	saveState = func(int) (func() error, error) {
		saved := mode
		return func() error { mode = saved; return nil }, nil
	}
	Open = func(io.Reader) (cancelreader.CancelReader, error) {
		mode = "changed by the reader"
		return nil, errors.New("no event for the console")
	}
	if _, err := For(tempFile(t)); err == nil {
		t.Fatal("For did not fail")
	}
	if mode != "cooked" {
		t.Errorf("after a failed open the terminal is %s, want cooked", mode)
	}
}

// Raw's restore runs once: a deferred call after the one in order changes
// nothing that came after the first.
func TestRawRestoresOnce(t *testing.T) {
	src := newFake()
	restores := 0
	oldOpen, oldSave, oldRaw := Open, saveState, makeRaw
	defer func() { Open, saveState, makeRaw = oldOpen, oldSave, oldRaw }()
	saveState = func(int) (func() error, error) { return func() error { restores++; return nil }, nil }
	Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	makeRaw = func(int) error { return nil }
	r, err := For(tempFile(t))
	if err != nil {
		t.Fatal(err)
	}
	restore, err := r.Raw()
	if err != nil {
		t.Fatal(err)
	}
	_ = restore()
	_ = restore()
	if restores != 1 {
		t.Errorf("the restore ran %d times, want 1", restores)
	}
}

// noCancelFake is a reader that yields bytes from a channel and whose
// Cancel always reports false, as fallbackCancelReader does.
type noCancelFake struct {
	bytes   chan byte
	entered chan struct{}
	mu      sync.Mutex
	closed  bool
}

func (n *noCancelFake) Read(p []byte) (int, error) {
	if n.entered != nil {
		n.entered <- struct{}{}
	}
	p[0] = <-n.bytes
	return 1, nil
}

func (*noCancelFake) Cancel() bool { return false }

func (n *noCancelFake) Close() error {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	return nil
}

// Release waits for the read goroutine itself to exit, and checks whether a
// read is still running rather than merely uncollected: a byte that arrived
// after a cancelled ReadKey on a reader that cannot cancel still releases
// cleanly, and a ReadKey that races with Release never steals its end.
func TestReleaseWaitsForTheReadGoroutine(t *testing.T) {
	f1, f2 := tempFile(t), tempFile(t)
	synctest.Test(t, func(t *testing.T) {
		oldOpen := Open
		defer func() { Open = oldOpen }()

		// A read that stayed in flight after ctx ended, then got a byte before
		// Release, is no longer inside Read: Release closes the reader cleanly
		// even when Cancel reports false.
		nc := &noCancelFake{bytes: make(chan byte, 2), entered: make(chan struct{}, 2)}
		Open = func(io.Reader) (cancelreader.CancelReader, error) { return nc, nil }
		r1, err := For(f1)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-nc.entered
			cancel()
		}()
		if _, err := r1.ReadKey(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadKey = %v, want context.Canceled", err)
		}
		nc.bytes <- 'x'
		synctest.Wait()
		if err := Release(f1); err != nil {
			t.Fatalf("Release after a completed uncollected read: %v", err)
		}
		nc.mu.Lock()
		closed := nc.closed
		nc.mu.Unlock()
		if !closed {
			t.Error("Release did not close the reader")
		}

		// A ReadKey that still waits when Release runs may take res, and
		// Release still sees the read goroutine exit without timing out.
		src := newFake()
		src.entered = make(chan struct{}, 2)
		Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
		r2, err := For(f2)
		if err != nil {
			t.Fatal(err)
		}
		readDone := make(chan error, 1)
		go func() {
			_, err := r2.ReadKey(context.Background())
			readDone <- err
		}()
		<-src.entered
		synctest.Wait()
		if err := Release(f2); err != nil {
			t.Fatalf("Release while ReadKey waited: %v", err)
		}
		synctest.Wait()
		select {
		case err := <-readDone:
			if err == nil {
				t.Error("ReadKey succeeded after Release")
			}
		default:
			t.Fatal("ReadKey did not end after Release")
		}
	})
}
