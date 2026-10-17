package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
)

// fakeBridge is a bridge over pipes that counts what it reads. Its answer
// to a ping is what pong returns, or nothing when pong returns nil. With
// hold set, it holds the answer until it reads the next frame, as a slow
// link would. It answers every request with an empty list.
type fakeBridge struct {
	pings, requests atomic.Int64
	hold            bool
}

// conn returns a connection to b.
func (b *fakeBridge) conn(t *testing.T, pong func([]byte) []byte) *Conn {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &Conn{Host: Host{Name: "box"}, stdin: inW, w: proto.NewWriter(inW),
		pending: map[uint64]chan proto.Response{}, stream: make(chan proto.Frame, 16), done: make(chan struct{})}
	go func() {
		defer outW.Close()
		r, w := bufio.NewReader(inR), proto.NewWriter(outW)
		var held []byte
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			if held != nil {
				_ = w.Write(proto.TPong, held)
				held = nil
			}
			switch f.Type {
			case proto.TPing:
				b.pings.Add(1)
				switch p := pong(f.Payload); {
				case p == nil:
				case b.hold:
					held = p
				default:
					_ = w.Write(proto.TPong, p)
				}
			case proto.TRequest:
				b.requests.Add(1)
				var req proto.Request
				_ = json.Unmarshal(f.Payload, &req)
				_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage("[]")})
			default:
			}
		}
	}()
	go c.readLoop(bufio.NewReader(outR))
	t.Cleanup(func() {
		_ = inW.Close()
		<-c.done
	})
	return c
}

func shortPongWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := pongWait
	pongWait = d
	t.Cleanup(func() { pongWait = old })
}

func roundTrip(t *testing.T, c *Conn) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := c.RoundTrip(ctx)
	if err != nil || d <= 0 {
		t.Fatalf("RoundTrip = %v, %v", d, err)
	}
	return d
}

// The bridge's own answer, with its clock, and an older bridge's, the ID
// alone, both time the round trip with a ping, and need no request.
func TestRoundTripPings(t *testing.T) {
	for name, pong := range map[string]func([]byte) []byte{
		"current": func(p []byte) []byte { return proto.Pong(p, diag.Now()) },
		"older":   func(p []byte) []byte { return p },
	} {
		t.Run(name, func(t *testing.T) {
			var b fakeBridge
			c := b.conn(t, pong)
			for range 3 {
				roundTrip(t, c)
			}
			if b.pings.Load() != 3 || b.requests.Load() != 0 || c.noPong.Load() {
				t.Fatalf("pings %d, requests %d, no pong %v: want 3 pings and no request", b.pings.Load(), b.requests.Load(), c.noPong.Load())
			}
			c.mu.Lock()
			waiting := len(c.timedPings)
			c.mu.Unlock()
			if waiting != 0 {
				t.Fatalf("%d pings still wait for their pong", waiting)
			}
		})
	}
}

// A bridge that does not answer pings is timed with a list of sessions,
// as before, and is not pinged again.
func TestRoundTripWithoutPongs(t *testing.T) {
	shortPongWait(t, 20*time.Millisecond)
	var b fakeBridge
	c := b.conn(t, func([]byte) []byte { return nil })
	roundTrip(t, c)
	if !c.noPong.Load() || b.requests.Load() != 1 {
		t.Fatalf("no pong %v, requests %d: want a list after the ping", c.noPong.Load(), b.requests.Load())
	}
	roundTrip(t, c)
	if b.pings.Load() != 1 || b.requests.Load() != 2 {
		t.Fatalf("pings %d, requests %d: want no second ping", b.pings.Load(), b.requests.Load())
	}
}

// On a slow link, the pong comes after RoundTrip asks for the list, but
// before the list. The round trip is the ping's, and the bridge stays one
// that answers pings.
func TestRoundTripSlowPong(t *testing.T) {
	const wait = 20 * time.Millisecond
	shortPongWait(t, wait)
	b := fakeBridge{hold: true} // the pong goes out only after the list's request comes in
	c := b.conn(t, func(p []byte) []byte { return proto.Pong(p, diag.Now()) })
	if d := roundTrip(t, c); d < wait {
		t.Fatalf("round trip %v, want the ping's, at least %v", d, wait)
	}
	if c.noPong.Load() || b.requests.Load() != 1 {
		t.Fatalf("no pong %v, requests %d", c.noPong.Load(), b.requests.Load())
	}
}

// A closed connection ends a RoundTrip that waits.
func TestRoundTripClosed(t *testing.T) {
	var b fakeBridge
	c := b.conn(t, func([]byte) []byte { return nil })
	go func() {
		for b.pings.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		c.fail(ErrClosed)
	}()
	if _, err := c.RoundTrip(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if _, err := c.RoundTrip(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("after the close: err = %v, want ErrClosed", err)
	}
}

// RoundTrip's pongs never reach the debug trace, and the trace's pongs
// still do.
func TestPongsGoWhereTheyBelong(t *testing.T) {
	tr := newTestTrace(t)
	c := &Conn{tr: &connTrace{t: tr, id: "ab.1"}}
	c.pong(proto.Pong(tr.ping(), diag.Now()))                   // the trace's ping
	c.pong(proto.Pong(proto.EncodePing(1|timedPingBit), 1))     // RoundTrip's, no longer waited for
	c.pong(proto.Pong(proto.EncodePing(7|timedPingBit), 1)[:8]) // an older bridge's echo of one
	c.pong(nil)                                                 // a heartbeat's
	recs := closeTrace(t, tr)
	if len(recs) != 1 || recs[0].Ev != "ping" || recs[0].Ping != 1 {
		t.Fatalf("records = %+v, want the trace's ping alone", recs)
	}
}

// A round trip shorter than a step of the clock, as on Windows, still
// counts as a sample.
func TestMeasuredIsNeverZero(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Nanosecond, time.Nanosecond} {
		if got := measured(d); got != minRoundTrip {
			t.Errorf("measured(%v) = %v, want %v", d, got, minRoundTrip)
		}
	}
	if got := measured(31 * time.Millisecond); got != 31*time.Millisecond {
		t.Errorf("measured(31ms) = %v", got)
	}
}
