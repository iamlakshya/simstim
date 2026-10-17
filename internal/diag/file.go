package diag

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

// DefaultMax is the size at which a trace file turns over. A busy agent makes
// a few hundred records a second, about 20 KiB, so 8 MiB keeps several
// minutes, and the old file as many more.
const DefaultMax = 8 << 20

// queueLen is how many records may wait for the writer before Add drops them.
const queueLen = 4096

// File writes records to a file, one JSON object a line, from a goroutine of
// its own. It turns the file over to the same name with ".1" added at its
// size limit. It writes only the records of its categories, every one at
// first. A nil *File writes nothing.
type File struct {
	path    string
	max     int64
	q       chan Record
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	cats    atomic.Uint32
	dropped atomic.Int64
	err     error // the first write error, read after done is closed
}

// Open opens path for appending, with mode 0600, and starts its writer.
// The file turns over at max bytes, or at DefaultMax when max is 0.
func Open(path string, max int64) (*File, error) {
	if max <= 0 {
		max = DefaultMax
	}
	f, size, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	t := &File{path: path, max: max, q: make(chan Record, queueLen), stop: make(chan struct{}), done: make(chan struct{})}
	t.cats.Store(uint32(AllCats))
	go t.run(f, size)
	return t, nil
}

// SetCats sets the categories whose records the file writes. A record
// about debug itself, or with no category, always goes in.
func (t *File) SetCats(c Cats) {
	if t != nil {
		t.cats.Store(uint32(c))
	}
}

// Cats are the categories whose records the file writes.
func (t *File) Cats() Cats {
	if t == nil {
		return 0
	}
	return Cats(t.cats.Load()) //nolint:gosec // G115: SetCats stores a Cats, which fits in 8 bits
}

func openAppend(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("open the trace file: %w", err)
	}
	// The mode applies to a new file only: make an old one private too.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("make the trace file private: %w", err)
	}
	// While it is open, the turnover of its folder leaves it alone. One
	// between the open and the lock may have removed it: open it again.
	markInUse(f)
	if !samePath(f, path) {
		f.Close()
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err != nil {
			return nil, 0, fmt.Errorf("open the trace file: %w", err)
		}
		markInUse(f)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("read the trace file's size: %w", err)
	}
	return f, st.Size(), nil
}

// Add queues r for writing. It never blocks: when the queue is full, it
// drops r and counts it, and a "dropped" record says how many were lost.
func (t *File) Add(r Record) {
	if t == nil || !Cats(t.cats.Load()).keeps(&r) { //nolint:gosec // G115: as in Cats
		return
	}
	select {
	case t.q <- r:
	default:
		t.dropped.Add(1)
	}
}

// Close writes the records already queued, closes the file, and returns
// the first error the writer met. Records added later are not written.
func (t *File) Close() error {
	if t == nil {
		return nil
	}
	t.once.Do(func() { close(t.stop) })
	<-t.done
	return t.err
}

// Path is the file's path.
func (t *File) Path() string { return t.path }

// run writes records until Close. It flushes whenever the queue is empty,
// so a reader of the file sees each record soon after it happened.
func (t *File) run(f *os.File, size int64) {
	w := &writer{path: t.path, max: t.max, f: f, size: size, bw: bufio.NewWriterSize(f, 64<<10)}
	for {
		select {
		case r := <-t.q:
			w.add(r)
			t.drain(w)
		case <-t.stop:
			t.drain(w)
			cerr := w.close()
			t.err = w.err
			if t.err == nil {
				t.err = cerr
			}
			close(t.done)
			return
		}
	}
}

// drain writes what is queued and the count of dropped records, then flushes.
func (t *File) drain(w *writer) {
	for {
		select {
		case r := <-t.q:
			w.add(r)
		default:
			if n := t.dropped.Swap(0); n > 0 {
				w.add(Record{T: Now(), Cat: CatAttach, Ev: EvDropped, Dropped: n})
			}
			w.flush()
			return
		}
	}
}

// writer is the file side of a File, used by its goroutine alone.
type writer struct {
	path string
	max  int64
	f    *os.File
	bw   *bufio.Writer
	size int64
	err  error
}

func (w *writer) add(r Record) {
	if w.err != nil {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		w.err = fmt.Errorf("encode a trace record: %w", err)
		return
	}
	line = append(line, '\n')
	if w.size > 0 && w.size+int64(len(line)) > w.max {
		w.turnOver()
		if w.err != nil {
			return
		}
	}
	if _, err := w.bw.Write(line); err != nil {
		w.err = fmt.Errorf("write the trace file: %w", err)
		return
	}
	w.size += int64(len(line))
}

// turnOver moves the full file to path+".1", replacing an older one, and
// starts an empty file.
func (w *writer) turnOver() {
	if err := w.close(); err != nil {
		w.err = err
		return
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		w.err = fmt.Errorf("turn over the trace file: %w", err)
		return
	}
	f, size, err := openAppend(w.path)
	if err != nil {
		w.err = err
		return
	}
	w.f, w.size = f, size
	w.bw.Reset(f)
}

func (w *writer) flush() {
	if w.err == nil {
		if err := w.bw.Flush(); err != nil {
			w.err = fmt.Errorf("write the trace file: %w", err)
		}
	}
}

// close flushes and closes the open file. It is safe to call twice.
func (w *writer) close() error {
	if w.f == nil {
		return nil
	}
	w.flush()
	err := w.f.Close()
	w.f = nil
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close the trace file: %w", err)
	}
	return nil
}
