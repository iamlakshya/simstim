package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The client's debug trace of an attach, on when the environment variable
// diag.EnvFile names a file. It records each read from stdin, each key sent,
// the first output after a key and the end of its write to the terminal,
// and a timed ping every tracePingEvery. It also asks the host to trace its
// side of the attach. With the trace off, each hook costs one nil check.

// tracePingEvery is the heartbeat while the trace is on: often enough to
// show the spread of the round trip.
const tracePingEvery = time.Second

// maxWaiting caps the pings and echoes a trace keeps while it waits for
// their answer or their write.
const maxWaiting = 64

// attachTrace is the trace of one Attach call. It lasts across reconnects.
type attachTrace struct {
	f     *diag.File
	base  string // random: each connection's trace ID adds its number to it
	conns atomic.Int64
	reads atomic.Int64 // reads from stdin so far
	taken atomic.Int64 // reads the attach loop has taken
	pings atomic.Uint64

	mu     sync.Mutex
	sent   map[uint64]int64 // pings waiting for their pong: when each was sent
	echoes []echoMark       // first output frames after keys, not yet written
}

// echoMark is the first output frame after a key, by its offset.
type echoMark struct {
	trace string
	key   int64
	off   int64
	recv  int64
}

// connTrace is the trace of one connection. Its keys count from 1, as the
// holder counts them.
type connTrace struct {
	t       *attachTrace
	id      string
	keys    atomic.Int64
	waiting atomic.Int64 // the newest key with no output after it yet, or 0
}

// openAttachTrace opens the trace file that diag.EnvFile names, or returns
// nil when it is not set.
func openAttachTrace(session string) (*attachTrace, error) {
	path := os.Getenv(diag.EnvFile)
	if path == "" {
		return nil, nil // the trace is off: not an error
	}
	f, err := diag.Open(path, diag.DefaultMax)
	if err != nil {
		return nil, &UserError{Msg: "cannot write the debug trace: " + err.Error(), Hint: "Set " + diag.EnvFile + " to a file you can write, or unset it."}
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	t := &attachTrace{f: f, base: hex.EncodeToString(b[:]), sent: map[uint64]int64{}}
	f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: "start", Side: "client", Session: session,
		Version: version.Version, Wall: time.Now().UnixNano()})
	return t, nil
}

// close closes the trace file. When a write failed, as on a full disk, it
// says so on w, so that a trace cut short does not pass unseen.
func (t *attachTrace) close(w io.Writer) {
	if t == nil {
		return
	}
	if err := t.f.Close(); err != nil {
		fmt.Fprintf(w, "Warning: the debug trace in %s is not complete: %v\nTry: make room on its disk, or set %s to a file on another disk.\n",
			t.f.Path(), err, diag.EnvFile)
	}
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
// to send to the host, or "".
func (c *Conn) startTrace() string {
	if c.tr == nil {
		return ""
	}
	c.tr.t.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatAttach, Ev: "conn", Trace: c.tr.id,
		HostTrace: slices.Contains(c.Features, "trace")})
	return c.tr.id
}

// sendKey writes a TInput frame and records it.
func (ct *connTrace) sendKey(w *proto.Writer, p []byte) error {
	start := diag.Now()
	err := w.Write(proto.TInput, p)
	end := diag.Now()
	k := ct.keys.Add(1)
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
	k := ct.waiting.Swap(0)
	off, _, err := proto.DecodeOutput(p)
	if k == 0 || err != nil {
		return
	}
	ct.t.mu.Lock()
	if len(ct.t.echoes) >= maxWaiting {
		ct.t.echoes = ct.t.echoes[1:]
	}
	ct.t.echoes = append(ct.t.echoes, echoMark{trace: ct.id, key: k, off: off, recv: now})
	ct.t.mu.Unlock()
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
