package client

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// The round trip to a host, as the launcher shows it
// (docs/contributing/ux-spec.md 2.3). The bridge answers a ping as soon as
// it reads it, so a timed ping measures the network and the bridge only,
// without the host's work for a request.

// timedPingBit marks the IDs of RoundTrip's pings. The debug trace counts
// its own pings from 1, so neither takes the other's pongs.
const timedPingBit = 1 << 63

// pongWait is how long RoundTrip waits for a pong before it checks that
// the bridge answers pings at all.
var pongWait = 2 * time.Second

// minRoundTrip is the shortest round trip that RoundTrip reports. Go's
// monotonic clock on Windows moves in steps of about half a millisecond, so
// a shorter round trip reads as 0. The launcher reads 0 as no sample, so a
// round trip below one step counts as this.
const minRoundTrip = time.Microsecond

// measured is a round trip that RoundTrip reports: never less than
// minRoundTrip.
func measured(d time.Duration) time.Duration { return max(d, minRoundTrip) }

// timedPing is one of RoundTrip's pings, on its way.
type timedPing struct {
	id   uint64
	sent time.Time
	pong chan time.Time // when its pong arrived
}

// RoundTrip measures the round trip to the host's bridge with a timed
// ping. A bridge that does not answer pings is timed with a list of
// sessions instead, from then on.
func (c *Conn) RoundTrip(ctx context.Context) (time.Duration, error) {
	if c.noPong.Load() {
		return c.listTime(ctx)
	}
	p, err := c.sendTimedPing()
	if err != nil {
		return 0, err
	}
	defer c.dropTimedPing(p.id)
	wait := time.NewTimer(pongWait)
	defer wait.Stop()
	select {
	case at := <-p.pong:
		return measured(at.Sub(p.sent)), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-c.done:
		return 0, ErrClosed
	case <-wait.C:
	}
	// No pong yet: a slow link, or a bridge that does not answer pings.
	// Every bridge answers a ping before it reads the next frame. So if a
	// pong comes at all, it comes before the answer to a request sent now.
	d, err := c.listTime(ctx)
	if err != nil {
		return 0, err
	}
	select {
	case at := <-p.pong:
		return measured(at.Sub(p.sent)), nil
	default:
		c.noPong.Store(true)
		return d, nil
	}
}

// listTime times a list of sessions: the round trip, and the host's work
// for the list.
func (c *Conn) listTime(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if _, err := c.Sessions(ctx); err != nil {
		return 0, err
	}
	return measured(time.Since(start)), nil
}

// sendTimedPing sends a timed ping that RoundTrip waits for.
func (c *Conn) sendTimedPing() (timedPing, error) {
	p := timedPing{id: c.nextTimedPing.Add(1) | timedPingBit, pong: make(chan time.Time, 1)}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return timedPing{}, c.err
	}
	if c.timedPings == nil {
		c.timedPings = map[uint64]chan time.Time{}
	}
	c.timedPings[p.id] = p.pong
	c.mu.Unlock()
	p.sent = time.Now()
	if err := c.w.Write(proto.TPing, proto.EncodePing(p.id)); err != nil {
		c.dropTimedPing(p.id)
		return timedPing{}, ErrClosed
	}
	return p, nil
}

// dropTimedPing stops waiting for a timed ping's pong.
func (c *Conn) dropTimedPing(id uint64) {
	c.mu.Lock()
	delete(c.timedPings, id)
	c.mu.Unlock()
}

// pong takes the answer to a ping: RoundTrip's own, or the debug trace's.
// An older bridge answers a timed ping with its ID alone, which is enough.
func (c *Conn) pong(p []byte) {
	id, _, ok := proto.DecodePong(p)
	if !ok || id&timedPingBit == 0 {
		if c.tr != nil {
			c.tr.t.pong(c.tr.id, p)
		}
		return
	}
	at := time.Now()
	c.mu.Lock()
	ch := c.timedPings[id]
	delete(c.timedPings, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- at // never blocks: it has room for the one pong
	}
}
