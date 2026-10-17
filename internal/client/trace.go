package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The client's debug trace of an attach, on while the client's debug
// (debug.go) records a category of an attach: attach, net, memory or
// runtime. It records each read from stdin, each key sent, the first output
// after a key and the end of its write to the terminal, and a timed ping
// every tracePingEvery, into the client's debug file, which keeps the
// records of the categories on. It also asks the host to trace its side of
// the attach, with the host's categories. With the trace off, each hook
// costs one nil check.

// tracePingEvery is the heartbeat while the trace is on: often enough to
// show the spread of the round trip.
const tracePingEvery = time.Second

// maxWaiting caps the pings and echoes a trace keeps while it waits for
// their answer or their write.
const maxWaiting = 64

// echoSlow is how much longer than the median of the attach's pings an echo
// may take before the client's flight recorder saves its window by itself.
// A slow link alone does not make it save.
const echoSlow = 250 * time.Millisecond

// keptRTTs is how many of the newest pings' round trips a trace keeps, for
// their median.
const keptRTTs = 16

// attachCats are the categories that an attach trace serves.
var attachCats = diag.CatsOf([]string{diag.CatAttach, diag.CatNet, diag.CatMemory, diag.CatRuntime})

// attachTrace is the trace of one Attach call. It lasts across reconnects.
type attachTrace struct {
	f     *diag.File
	host  diag.Cats // the host's categories, which the attach asks the host for
	base  string    // random: each connection's trace ID adds its number to it
	conns atomic.Int64
	reads atomic.Int64 // reads from stdin so far
	taken atomic.Int64 // reads the attach loop has taken
	pings atomic.Uint64

	mu     sync.Mutex
	sent   map[uint64]int64 // pings waiting for their pong: when each was sent
	echoes []echoMark       // first output frames after keys, not yet written
	rtts   []int64          // the newest pings' round trips
}

// echoMark is the first output frame after a key, by its offset.
type echoMark struct {
	trace string
	key   int64
	off   int64
	sent  int64 // when the key was sent
	recv  int64
}

// connTrace is the trace of one connection. Its keys count from 1, as the
// holder counts them.
type connTrace struct {
	t       *attachTrace
	id      string
	keys    atomic.Int64
	waiting atomic.Int64 // the newest key with no output after it yet, or 0
	sentAt  atomic.Int64 // when that key was sent
}

// openAttachTrace starts the trace of an attach to session, or returns nil
// when the client's debug records no category of an attach.
func openAttachTrace(session string) *attachTrace {
	run := debugRun.Load()
	if run == nil || run.Cats()&attachCats == 0 {
		return nil
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	t := &attachTrace{f: run.File, host: run.Cats() & diag.HostCats, base: hex.EncodeToString(b[:]), sent: map[uint64]int64{}}
	// Each attach starts with its own start record: read numbers count
	// from 1 again after it.
	t.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: diag.EvStart, Side: "client", Session: session,
		Version: version.Version, Wall: time.Now().UnixNano()})
	return t
}

type traceCtxKey struct{}

// withTrace makes the connections that Dial opens with ctx carry t.
func withTrace(ctx context.Context, t *attachTrace) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, traceCtxKey{}, t)
}

// newConnTrace starts the trace of a new connection, when ctx carries one.
func newConnTrace(ctx context.Context) *connTrace {
	t, _ := ctx.Value(traceCtxKey{}).(*attachTrace)
	if t == nil {
		return nil
	}
	t.mu.Lock()
	t.echoes = nil // the old connection's frames will not be written
	t.mu.Unlock()
	return &connTrace{t: t, id: fmt.Sprintf("%s.%d", t.base, t.conns.Add(1))}
}

// pingEvery is the heartbeat's interval.
func (t *attachTrace) pingEvery() time.Duration {
	if t == nil {
		return pingEvery
	}
	return tracePingEvery
}

// stdin records a read of n bytes from the terminal.
func (t *attachTrace) stdin(n int) {
	if t == nil {
		return
	}
	now := diag.Now()
	t.f.Add(diag.Record{T: now, Cat: diag.CatAttach, Ev: "stdin", Read: t.reads.Add(1), N: n})
}

// took counts a read that the attach loop takes from the reader. Reads go
// through in order, so the count names the read that the next keys come from.
func (t *attachTrace) took() {
	if t != nil {
		t.taken.Add(1)
	}
}

// startTrace records the start of an attach on c, and returns the trace ID
// and the host's categories to send to the host, or "" and nil when the
// host records nothing.
func (c *Conn) startTrace() (string, []string) {
	if c.tr == nil {
		return "", nil
	}
	host := c.tr.t.host
	c.tr.t.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: "conn", Trace: c.tr.id,
		HostTrace: host != 0 && slices.Contains(c.Features, api.FeatureTrace)})
	if host == 0 {
		return "", nil
	}
	return c.tr.id, host.List()
}

// sendKey writes a TInput frame and records it.
func (ct *connTrace) sendKey(w *proto.Writer, p []byte) error {
	start := diag.Now()
	err := w.Write(proto.TInput, p)
	end := diag.Now()
	k := ct.keys.Add(1)
	ct.sentAt.Store(end)
	ct.waiting.Store(k)
	ct.t.f.Add(diag.Record{T: end, Cat: diag.CatAttach, Ev: "key", Trace: ct.id, Key: k, Read: ct.t.taken.Load(),
		N: len(p), Write: end - start})
	return err
}

// arrived notes an output frame from the reader: the first one after a key
// is that key's echo, which drawn records once it is written.
func (ct *connTrace) arrived(p []byte) {
	if ct.waiting.Load() == 0 {
		return
	}
	now := diag.Now()
	sent := ct.sentAt.Load()
	k := ct.waiting.Swap(0)
	off, _, err := proto.DecodeOutput(p)
	if k == 0 || err != nil {
		return
	}
	ct.t.mu.Lock()
	if len(ct.t.echoes) >= maxWaiting {
		ct.t.echoes = ct.t.echoes[1:]
	}
	ct.t.echoes = append(ct.t.echoes, echoMark{trace: ct.id, key: k, off: off, sent: sent, recv: now})
	slow := len(ct.t.rtts) > 0 && now-sent > medianOf(ct.t.rtts)+int64(echoSlow)
	ct.t.mu.Unlock()
	if slow {
		debugRuntime().AutoSave("echo")
	}
}

// medianOf is the median of a few round trips.
func medianOf(rtts []int64) int64 {
	s := slices.Clone(rtts)
	slices.Sort(s)
	return s[len(s)/2]
}

// drawn records the echoes in the output frame at off, which the attach
// has just written to the terminal.
func (t *attachTrace) drawn(off int64, n int) {
	if t == nil {
		return
	}
	now := diag.Now()
	t.mu.Lock()
	var done []echoMark
	for len(t.echoes) > 0 && t.echoes[0].off <= off {
		if t.echoes[0].off == off {
			done = append(done, t.echoes[0])
		}
		t.echoes = t.echoes[1:]
	}
	t.mu.Unlock()
	for _, e := range done {
		t.f.Add(diag.Record{T: now, Cat: diag.CatAttach, Ev: "echo", Trace: e.trace, Key: e.key, N: n, Recv: e.recv})
	}
}

// ping returns the payload of a timed ping, and notes when it was sent.
func (t *attachTrace) ping() []byte {
	id := t.pings.Add(1)
	t.mu.Lock()
	if len(t.sent) >= maxWaiting {
		clear(t.sent) // pongs that never came
	}
	t.sent[id] = diag.Now()
	t.mu.Unlock()
	return proto.EncodePing(id)
}

// pong records the round trip of a timed ping on the connection with the
// trace ID trace, and the offset between the clocks when the host sent its
// time. Each connection may reach another host, or a host that restarted.
func (t *attachTrace) pong(trace string, p []byte) {
	now := diag.Now()
	id, host, ok := proto.DecodePong(p)
	if !ok {
		return
	}
	t.mu.Lock()
	sent, found := t.sent[id]
	delete(t.sent, id)
	if found {
		if len(t.rtts) >= keptRTTs {
			t.rtts = t.rtts[1:]
		}
		t.rtts = append(t.rtts, now-sent)
	}
	t.mu.Unlock()
	if !found {
		return
	}
	r := diag.Record{T: now, Cat: diag.CatNet, Ev: "ping", Trace: trace, Ping: id, RTT: now - sent, Host: host}
	if host != 0 {
		r.Offset = clockOffset(sent, now, host)
	}
	t.f.Add(r)
}

// clockOffset is the host's clock minus the client's, from one ping, as NTP
// calculates it (RFC 5905): the host read its clock at host, between sent and
// recv on the client's clock. It takes the network delay to be the same each
// way, so its error is at most half the round trip.
func clockOffset(sent, recv, host int64) int64 {
	return host - (sent+recv)/2
}
