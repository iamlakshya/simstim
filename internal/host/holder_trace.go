package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The holder's debug trace. A client turns it on for its own attach
// (proto.Attach.Trace), and `debug start` for the session (holder_debug.go).
// It runs while either asks for it. The holder then writes records about
// keys, its lock, its screen model, the session's memory and Go's runtime
// to the session's debug file (paths.DebugTrace), with its debug lines.
//
// The trace adds only clock reads to the time the holder holds h.mu: one
// when input(), output() or tick() gets the lock, and two around the screen
// model in output(). It builds its records after the unlock, and hands them
// to the file's own goroutine. With the trace off, each hook costs one nil
// check.

// holderDebug is the debug mode's state in a holder.
type holderDebug struct {
	mu sync.Mutex // orders turning the trace on and off
	// traced are the traced attaches, with their host categories.
	traced map[*hclient]diag.Cats
	// session are the categories that `debug start` turned on, until until.
	session diag.Cats
	until   time.Time
	timer   *time.Timer
	// lines takes the holder's debug lines, into the trace's file while it
	// runs.
	lines *diag.Lines
	tr    atomic.Pointer[holderTrace]
	// busy is set while a profile or a save on request runs.
	busy atomic.Bool
	// before is the last snapshot of the unit's processes at the start of a
	// memory warning, with the memory category on (holder_oomsnap.go).
	before atomic.Pointer[procSnap]
	// ended is set once the holder exits: no debug starts after it.
	ended bool
}

// holderTrace is one run of the trace, from when a traced attach or `debug
// start` turns it on to when nothing asks for it any more.
type holderTrace struct {
	f   *diag.File
	run *diag.Run // the run that owns f; nil in some tests
	// all is set while the session's debug traces every attached client's
	// keys. A client with no trace ID of its own then gets one from base.
	all  atomic.Bool
	base string
	ids  atomic.Int64
	// The memory sampler, while the memory category is on.
	memStop context.CancelFunc
	sampled chan struct{}
	// pending is the newest key written to the PTY whose next read is not
	// seen yet, or nil.
	pending atomic.Pointer[keyRef]
}

// keyRef names a key whose PTY write ended at at.
type keyRef struct {
	trace string
	key   int64
	at    int64
}

// clientTrace is the trace's state in one client, used by its serve
// goroutine alone.
type clientTrace struct {
	id     string // the attach's trace ID, or "" when it is not traced
	own    bool   // the holder gave the ID, for the session's debug
	keys   int64  // TInput frames so far
	bridge int64  // when the bridge read the next key, from TTrace
}

// memEvery is how often the trace samples the session's memory.
const memEvery = time.Second

// lockWaitSave is how long a key may wait for h.mu before the flight
// recorder saves its window by itself.
const lockWaitSave = 100 * time.Millisecond

// partOneCats are the categories of a traced attach from a client that
// names none: part 1 of the debug mode recorded these.
var partOneCats = diag.CatsOf([]string{diag.CatAttach, diag.CatMemory})

// traceAttach starts tracing c's attach when id is set, with the host
// categories cats, and the holder's trace with it. A client that attaches
// again on the same connection ends its old trace first.
func (h *Holder) traceAttach(c *hclient, id string, cats []string) {
	h.traceOff(c)
	if id == "" {
		return
	}
	want := diag.CatsOf(cats) & diag.HostCats
	if len(cats) == 0 {
		want = partOneCats
	}
	if want == 0 {
		return
	}
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	if h.dbg.traced == nil {
		h.dbg.traced = map[*hclient]diag.Cats{}
	}
	h.dbg.traced[c] = want
	tr := h.applyDebug("detach")
	if tr == nil {
		delete(h.dbg.traced, c)
		return
	}
	c.tr = clientTrace{id: id}
	tr.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: diag.EvStart, Side: "host", Trace: id,
		Session: h.id, Version: version.Version, Wall: time.Now().UnixNano(), Cats: want.String()})
}

// traceOff stops tracing c's attach, and the holder's trace after the last
// traced client, unless the session's debug is on.
func (h *Holder) traceOff(c *hclient) {
	if c.tr.id == "" {
		return
	}
	own := c.tr.own
	c.tr = clientTrace{}
	if own {
		return
	}
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	if _, ok := h.dbg.traced[c]; !ok {
		return // `debug stop` ended it already
	}
	delete(h.dbg.traced, c)
	h.applyDebug("detach")
}

// applyDebug starts, changes or stops the trace, to record what the
// session's debug and the traced attaches ask for. It returns the trace, or
// nil when it is off. The caller holds h.dbg.mu.
func (h *Holder) applyDebug(reason string) *holderTrace {
	cats := h.dbg.session
	for _, c := range h.dbg.traced {
		cats |= c
	}
	if h.dbg.ended {
		cats = 0 // the holder exits: no trace opens now
	}
	tr := h.dbg.tr.Load()
	if cats == 0 {
		if tr != nil {
			h.dbg.tr.Store(nil)
			h.closeTrace(tr, reason)
		}
		return nil
	}
	if tr == nil {
		if tr = h.openTrace(cats); tr == nil {
			return nil
		}
		defer h.dbg.tr.Store(tr) // the other records of a new trace follow its start record
	} else {
		tr.run.SetCats(cats)
	}
	tr.all.Store(h.dbg.session.Has(diag.CatAttach))
	h.sampleWhile(tr, cats.Has(diag.CatMemory))
	return tr
}

// openTrace opens the session's debug file, with the categories cats.
func (h *Holder) openTrace(cats diag.Cats) *holderTrace {
	if h.dbg.lines == nil {
		h.dbg.lines = diag.NewLines()
	}
	run, err := diag.StartRun(cats, diag.RunOptions{
		Path:    paths.DebugTrace(h.id),
		Start:   diag.Record{Side: "host", Session: h.id, Version: version.Version, Wall: time.Now().UnixNano()},
		Runtime: diag.RuntimeOptions{Dir: paths.SessionDebugDir(h.id), Max: diag.SessionFolderMax, Flight: true},
		Lines:   h.dbg.lines,
	})
	if err != nil {
		h.log.Warn("debug trace not started", "err", err)
		return nil
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	h.log.Info("debug trace on", "file", run.File.Path(), "cats", cats.String())
	return &holderTrace{f: run.File, run: run, base: "h" + hex.EncodeToString(b[:])}
}

// closeTrace stops the trace's memory sampler and closes its file.
func (h *Holder) closeTrace(tr *holderTrace, reason string) {
	h.sampleWhile(tr, false)
	if err := tr.run.Stop(reason); err != nil {
		h.log.Warn("debug trace not written", "err", err)
	}
	h.log.Info("debug trace off", "reason", reason)
}

// sampleWhile starts or stops the trace's memory sampler.
func (h *Holder) sampleWhile(tr *holderTrace, on bool) {
	switch {
	case on && tr.memStop == nil:
		ctx, cancel := context.WithCancel(context.Background())
		tr.memStop, tr.sampled = cancel, make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			h.sampleMemory(ctx, tr)
		}(tr.sampled)
	case !on && tr.memStop != nil:
		tr.memStop()
		<-tr.sampled
		tr.memStop = nil
	}
}

// tickLate is how late the status tick may come before the flight
// recorder saves its window by itself: a late tick means that the holder
// itself stalled, as under memory pressure.
const tickLate = 500 * time.Millisecond

// noteTick saves the flight recorder's window when this tick came more
// than tickLate after its time, with last the time of the tick before. It
// returns the time of this one.
func (h *Holder) noteTick(last time.Time) time.Time {
	now := time.Now()
	if tr := h.dbg.tr.Load(); tr != nil && now.Sub(last) > time.Second+tickLate {
		tr.autoSave("tick")
	}
	return now
}

// autoSave saves the flight recorder's window, when it runs, at most once a
// minute.
func (tr *holderTrace) autoSave(reason string) {
	if tr.run != nil {
		tr.run.Runtime().AutoSave(reason)
	}
}

// bridgeRead keeps the time from a TTrace frame for c's next key.
func (c *hclient) bridgeRead(p []byte) { c.tr.bridge = proto.DecodeTime(p) }

// lockTimes are the times around one hold of h.mu, while the trace is on.
type lockTimes struct {
	tr    *holderTrace
	asked int64 // when the holder asked for the lock
	got   int64 // when it got it
}

// lockTimed takes h.mu. With the trace on, it also notes when it asked for
// the lock and when it got it.
func (h *Holder) lockTimed() lockTimes {
	tr := h.dbg.tr.Load()
	if tr == nil {
		h.mu.Lock()
		return lockTimes{}
	}
	asked := diag.Now()
	h.mu.Lock()
	return lockTimes{tr: tr, asked: asked, got: diag.Now()}
}

// now is the time with the trace on, and 0 with it off.
func (l lockTimes) now() int64 {
	if l.tr == nil {
		return 0
	}
	return diag.Now()
}

// since is the time from t with the trace on, and 0 with it off.
func (l lockTimes) since(t int64) int64 {
	if l.tr == nil {
		return 0
	}
	return diag.Now() - t
}

// key records a key that c sent, after its PTY write. The caller has
// released h.mu. While the session's debug traces every client, a client
// that is not traced gets an ID from the holder.
func (l lockTimes) key(c *hclient, n int) {
	if l.tr == nil {
		return
	}
	if l.got-l.asked > int64(lockWaitSave) {
		l.tr.autoSave("lock")
	}
	all := l.tr.all.Load()
	switch {
	case c.tr.id == "" && all:
		c.tr = clientTrace{id: fmt.Sprintf("%s.%d", l.tr.base, l.tr.ids.Add(1)), own: true}
	case c.tr.id == "", c.tr.own && !all:
		return
	}
	c.tr.keys++
	ref := &keyRef{trace: c.tr.id, key: c.tr.keys, at: diag.Now()}
	l.tr.f.Add(diag.Record{T: ref.at, Cat: diag.CatAttach, Ev: "key", Trace: ref.trace, Key: ref.key, N: n,
		Recv: l.asked, Bridge: c.tr.bridge, Wait: l.got - l.asked})
	c.tr.bridge = 0
	l.tr.pending.Store(ref)
}

// read records one read of the agent's output: how long output() waited
// for h.mu and held it, and the time in the screen model. The first read
// that returned after a key's PTY write ended names that key. A read that
// returned before it, and waited for the lock while input() held it, is
// not the key's. The caller has released h.mu.
func (l lockTimes) read(n int, feed int64) {
	if l.tr == nil {
		return
	}
	r := diag.Record{T: l.asked, Cat: diag.CatAttach, Ev: "read", N: n, Wait: l.got - l.asked, Hold: diag.Now() - l.got, Feed: feed}
	if k := l.tr.pending.Load(); k != nil && l.asked >= k.at && l.tr.pending.CompareAndSwap(k, nil) {
		r.Trace, r.Key = k.trace, k.key
	}
	l.tr.f.Add(r)
}

// tick records how long tick() waited for h.mu and held it. The caller has
// released h.mu.
func (l lockTimes) tick() {
	if l.tr == nil {
		return
	}
	l.tr.f.Add(diag.Record{T: l.asked, Cat: diag.CatAttach, Ev: "tick", Wait: l.got - l.asked, Hold: diag.Now() - l.got})
}

// sampleMemory writes a sample of the session unit's memory files every
// memEvery until ctx ends. A holder outside its own unit (with no systemd)
// writes no samples.
func (h *Holder) sampleMemory(ctx context.Context, tr *holderTrace) {
	dir := unitCgroupDir(h.id)
	if dir == "" {
		return
	}
	t := time.NewTicker(memEvery)
	defer t.Stop()
	for {
		tr.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatMemory, Ev: "sample", Mem: readMemory(dir)})
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
