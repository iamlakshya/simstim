package diag

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"sync"
	"sync/atomic"
	"time"
)

// The flight recorder's window (decision 0025): about twice the few seconds
// that a lag lasts, and at most about 8 MiB. MaxBytes takes precedence, so a
// busy process may keep less than FlightMinAge.
const (
	FlightMinAge   = 10 * time.Second
	FlightMaxBytes = 8 << 20
)

// AutoSaveEvery is how often a process may save its flight recorder's
// window by itself, at most.
const AutoSaveEvery = time.Minute

// AutoMark starts the name of a window that a process saved by itself, as
// in auto-flight-<time>.trace. KeepAuto is how many of them a folder keeps,
// the newest: under steady pressure, a process saves one each minute.
const (
	AutoMark = "auto-"
	KeepAuto = 2
)

// ErrBusy is the error of a save while another save runs in the process.
var ErrBusy = errors.New("a save of the flight recorder is already running")

// ErrNoRecorder is the error of a save with no flight recorder running.
var ErrNoRecorder = errors.New("the flight recorder is not running")

// RuntimeOptions says what a Runtime does besides its metrics, and where
// its files go.
type RuntimeOptions struct {
	// Dir is the folder of the profiles and the saved windows, and Max its
	// limit. Prefix starts each file's name, such as "client-20261016-185712-4242-".
	Dir    string
	Max    int64
	Prefix string
	// CPU profiles the CPU for the whole run, and writes a heap profile at
	// its end: Prefix+"cpu.pprof" and Prefix+"heap.pprof".
	CPU bool
	// Flight runs the flight recorder.
	Flight bool
	// Log takes the runtime's own debug lines, or nil.
	Log *slog.Logger
}

// Runtime records Go's own view of this process while the runtime category
// is on: three runtime metrics each second, and as its options ask, a CPU
// profile of the run and the flight recorder.
type Runtime struct {
	f      *File
	o      RuntimeOptions
	cancel context.CancelFunc
	done   chan struct{}
	cpu    *bytes.Buffer // the CPU profile so far, or nil
	fr     *trace.FlightRecorder

	saveMu   sync.Mutex // one save at a time
	lastAuto atomic.Int64
}

// StartRuntime starts recording into f. A part that cannot start, such as a
// second CPU profile in one process, is left out, with a debug line in f.
func StartRuntime(f *File, o RuntimeOptions) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	if o.Log == nil {
		o.Log = Discard
	}
	r := &Runtime{f: f, o: o, cancel: cancel, done: make(chan struct{})}
	if o.CPU {
		buf := &bytes.Buffer{}
		if err := pprof.StartCPUProfile(buf); err == nil {
			r.cpu = buf
		} else {
			o.Log.Warn("no CPU profile of this run", "err", err)
		}
	}
	if o.Flight {
		fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: FlightMinAge, MaxBytes: FlightMaxBytes})
		if err := fr.Start(); err == nil {
			r.fr = fr
		} else {
			o.Log.Warn("no flight recorder", "err", err)
		}
	}
	go func() {
		defer close(r.done)
		sampleMetrics(ctx, f)
	}()
	return r
}

// Stop stops the metrics and the flight recorder. With CPU in its options,
// it writes the run's CPU profile and a heap profile.
func (r *Runtime) Stop() error {
	if r == nil {
		return nil
	}
	r.cancel()
	<-r.done
	var errs []error
	if r.cpu != nil {
		pprof.StopCPUProfile()
		errs = append(errs, r.write(r.o.Prefix+"cpu.pprof", "run", r.cpu.Bytes()))
	}
	if r.o.CPU {
		var heap bytes.Buffer
		if err := pprof.Lookup("heap").WriteTo(&heap, 0); err == nil {
			errs = append(errs, r.write(r.o.Prefix+"heap.pprof", "run", heap.Bytes()))
		}
	}
	if r.fr != nil {
		r.saveMu.Lock()
		r.fr.Stop()
		r.fr = nil
		r.saveMu.Unlock()
	}
	return errors.Join(errs...)
}

// write writes a file into the runtime's folder, and records the save.
func (r *Runtime) write(name, reason string, data []byte) error {
	path := filepath.Join(r.o.Dir, name)
	if err := WriteFile(path, data); err != nil {
		return err
	}
	r.f.Add(Record{T: Now(), Cat: CatRuntime, Ev: EvSave, File: name, Reason: reason, N: len(data)})
	Prune(r.o.Dir, r.o.Max)
	return nil
}

// Flight reports whether the flight recorder runs.
func (r *Runtime) Flight() bool {
	if r == nil {
		return false
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	return r.fr != nil
}

// Save writes the flight recorder's window to a new file whose name ends in
// flight-<time>.trace, and returns its path. It is a save on request, such
// as `debug save`: the folder's turnover may then remove older files.
func (r *Runtime) Save(reason string) (string, error) {
	return r.save(reason, false)
}

// save writes the window. An automatic save gets a name with AutoMark, and
// removes only automatic saves beyond the newest KeepAuto: it never pushes
// out a file made on request.
func (r *Runtime) save(reason string, auto bool) (string, error) {
	if r == nil {
		return "", ErrNoRecorder
	}
	if !r.saveMu.TryLock() {
		return "", ErrBusy
	}
	defer r.saveMu.Unlock()
	if r.fr == nil {
		return "", ErrNoRecorder
	}
	mark := ""
	if auto {
		mark = AutoMark
	}
	path := FreePath(r.o.Dir, r.o.Prefix+mark+"flight-"+TimeName(time.Now()), ".trace")
	f, err := CreatePrivate(path)
	if err != nil {
		return "", err
	}
	n, err := r.fr.WriteTo(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("save the flight recorder's window: %w", err)
	}
	r.f.Add(Record{T: Now(), Cat: CatRuntime, Ev: EvSave, File: filepath.Base(path), Reason: reason, N: int(n)})
	if auto {
		PruneAuto(r.o.Dir, KeepAuto)
	} else {
		Prune(r.o.Dir, r.o.Max)
	}
	return path, nil
}

// AutoSave saves the flight recorder's window in the background, unless
// the process saved one by itself less than AutoSaveEvery ago.
func (r *Runtime) AutoSave(reason string) {
	if r == nil {
		return
	}
	now := Now()
	last := r.lastAuto.Load()
	if last != 0 && now-last < int64(AutoSaveEvery) || !r.lastAuto.CompareAndSwap(last, now) {
		return
	}
	go func() { _, _ = r.save(reason, true) }()
}

// FreePath is dir/base+ext, or dir/base-2+ext and so on when that exists.
func FreePath(dir, base, ext string) string {
	path := filepath.Join(dir, base+ext)
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
}

// Profiles that a holder writes on request.
const (
	ProfileCPU       = "cpu"
	ProfileHeap      = "heap"
	ProfileGoroutine = "goroutine"
)

// The time of a CPU profile on request: by default, and at most.
const (
	ProfileDefault = 30 * time.Second
	ProfileMax     = 5 * time.Minute
)

// WriteProfile writes a profile of this process to a new file in dir, as
// gzipped protocol buffers, and returns its path: kind-<time>.pprof. A CPU
// profile runs for d, or until ctx ends.
func WriteProfile(ctx context.Context, kind string, d time.Duration, dir string) (string, error) {
	var buf bytes.Buffer
	switch kind {
	case ProfileCPU:
		if err := pprof.StartCPUProfile(&buf); err != nil {
			return "", fmt.Errorf("start a CPU profile: %w", err)
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
		pprof.StopCPUProfile()
	case ProfileHeap, ProfileGoroutine:
		if err := pprof.Lookup(kind).WriteTo(&buf, 0); err != nil {
			return "", fmt.Errorf("write a %s profile: %w", kind, err)
		}
	default:
		return "", fmt.Errorf("no profile %q: the profiles are cpu, heap and goroutine", kind)
	}
	path := FreePath(dir, kind+"-"+TimeName(time.Now()), ".pprof")
	if err := WriteFile(path, buf.Bytes()); err != nil {
		return "", err
	}
	return path, nil
}
