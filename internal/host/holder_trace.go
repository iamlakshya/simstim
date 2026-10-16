package host

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The holder's debug trace. A client turns it on for its own attach
// (proto.Attach.Trace), and it stays on while a traced client is attached.
// The holder then writes records about keys, its lock, its screen model and
// the session's memory to the session's debug file (paths.DebugTrace).
//
// The trace adds only clock reads to the time the holder holds h.mu: one
// when input(), output() or tick() gets the lock, and two around the screen
// model in output(). It builds its records after the unlock, and hands them
// to the file's own goroutine. With the trace off, each hook costs one nil
// check.

// holderDebug is the trace's state in a holder.
type holderDebug struct {
	mu sync.Mutex // orders turning the trace on and off
	n  int        // traced clients attached
	tr atomic.Pointer[holderTrace]
}

// holderTrace is one run of the trace, from the first traced attach to the
// last traced detach.
type holderTrace struct {
	f       *diag.File
	cancel  context.CancelFunc // stops the memory sampler
	sampled chan struct{}      // closed when the memory sampler has stopped
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
	keys   int64  // TInput frames so far
	bridge int64  // when the bridge read the next key, from TTrace
}

// memEvery is how often the trace samples the session's memory.
const memEvery = time.Second

// traceAttach starts tracing c's attach when id is set, and the holder's
// trace with it. A client that attaches again on the same connection ends
// its old trace first.
func (h *Holder) traceAttach(c *hclient, id string) {
	h.traceOff(c)
	if id == "" {
		return
	}
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	tr := h.dbg.tr.Load()
	fresh := tr == nil
	if fresh {
		f, err := diag.Open(paths.DebugTrace(h.id), diag.DefaultMax)
		if err != nil {
			h.log.Warn("debug trace not started", "err", err)
			return
		}
		tr = &holderTrace{f: f, sampled: make(chan struct{})}
	}
	h.dbg.n++
	c.tr = clientTrace{id: id}
	tr.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: "start", Side: "host", Trace: id,
		Session: h.id, Version: version.Version, Wall: time.Now().UnixNano()})
	if fresh {
		// The other records of a new trace follow its start record.
		ctx, cancel := context.WithCancel(context.Background())
		tr.cancel = cancel
		h.dbg.tr.Store(tr)
		go func() {
			defer close(tr.sampled)
			h.sampleMemory(ctx, tr)
		}()
		h.log.Info("debug trace on", "file", tr.f.Path())
	}
}

// traceOff stops tracing c's attach, and the holder's trace after the last
// traced client.
func (h *Holder) traceOff(c *hclient) {
	if c.tr.id == "" {
		return
	}
	c.tr.id = ""
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	h.dbg.n--
	if h.dbg.n > 0 {
		return
	}
	if tr := h.dbg.tr.Swap(nil); tr != nil {
		tr.cancel()
		<-tr.sampled
		if err := tr.f.Close(); err != nil {
			h.log.Warn("debug trace not written", "err", err)
		}
		h.log.Info("debug trace off")
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
// released h.mu.
func (l lockTimes) key(c *hclient, n int) {
	if l.tr == nil || c.tr.id == "" {
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
