package diag

import (
	"errors"
	"log/slog"
	"sync"
)

// Run is one run of the debug mode: a debug file and its categories, the
// debug lines that go into it, and with the runtime category on, Go's own
// view of the process. The client has one run for its process, a bridge one
// for each connection, and a holder one for its session.
type Run struct {
	File *File
	// Log takes the run's debug lines. Each line goes into File while the
	// run lasts.
	Log *slog.Logger

	lines *Lines
	o     RuntimeOptions

	mu sync.Mutex // orders SetCats and Stop
	rt *Runtime
}

// RunOptions starts a Run.
type RunOptions struct {
	// Path is the debug file, which turns over at Max bytes (DefaultMax when
	// 0).
	Path string
	Max  int64
	// Start is the start record. Its T and Cats are filled in.
	Start Record
	// Runtime says where the runtime category writes, and what it runs.
	Runtime RuntimeOptions
	// Lines, when set, sends its lines to the run's file while it lasts.
	Lines *Lines
}

// StartRun opens the debug file, writes the start record, and turns on the
// categories cats.
func StartRun(cats Cats, o RunOptions) (*Run, error) {
	f, err := Open(o.Path, o.Max)
	if err != nil {
		return nil, err
	}
	lines := o.Lines
	if lines == nil {
		lines = NewLines()
	}
	r := &Run{File: f, Log: slog.New(lines), lines: lines, o: o.Runtime}
	if r.o.Log == nil {
		r.o.Log = r.Log
	}
	st := o.Start
	st.T, st.Ev, st.Cats = Now(), EvStart, cats.String()
	f.SetCats(cats)
	f.Add(st)
	lines.SetFile(f)
	r.SetCats(cats)
	if r.o.Dir != "" {
		Prune(r.o.Dir, r.o.Max)
	}
	return r, nil
}

// On reports whether the run records the category name. A nil Run records
// nothing.
func (r *Run) On(name string) bool { return r != nil && r.File.Cats().Has(name) }

// Cats are the categories on.
func (r *Run) Cats() Cats {
	if r == nil {
		return 0
	}
	return r.File.Cats()
}

// SetCats turns on the categories cats, and the others off. It starts or
// stops the runtime category's work with it.
func (r *Run) SetCats(cats Cats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.File.SetCats(cats)
	switch on := cats.Has(CatRuntime); {
	case on && r.rt == nil:
		r.rt = StartRuntime(r.File, r.o)
	case !on && r.rt != nil:
		if err := r.rt.Stop(); err != nil {
			r.Log.Warn("debug: the runtime's files are not complete", "err", err)
		}
		r.rt = nil
	}
}

// Runtime is the runtime category's work while it is on, or nil.
func (r *Run) Runtime() *Runtime {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rt
}

// Stop ends the run: it writes a stop record with reason, stops the
// runtime's work, and closes the file. It returns the first error that a
// write met, as on a full disk.
func (r *Run) Stop(reason string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	rt := r.rt
	r.rt = nil
	r.mu.Unlock()
	rerr := rt.Stop()
	r.File.Add(Record{T: Now(), Ev: EvStop, Reason: reason})
	r.lines.SetFile(nil)
	return errors.Join(rerr, r.File.Close())
}
