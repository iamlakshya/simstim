package host

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// The bound of a client's frames that wait in the bridge for a holder. A
// larger frame waits until the queue is empty, then passes alone.
const (
	maxBridgeQueued = 512 << 10
	maxBridgeFrames = 256
)

// bridgeDrain bounds how long queued frames may take to reach a holder after
// the client detaches or closes its input.
var bridgeDrain = 200 * time.Millisecond

// bridgeHolder carries frames from the bridge to one attached holder
// connection on its own goroutine, so a holder or sandbox that stops reading
// input never freezes the bridge's own loop on the write call.
type bridgeHolder struct {
	conn    net.Conn
	in      chan proto.Frame
	queued  atomic.Int64
	room    chan struct{}
	done    chan struct{}
	drained chan struct{}
	once    sync.Once
	endOnce sync.Once
	soon    sync.Once
	drain   time.Duration
}

func newBridgeHolder(conn net.Conn, hw *proto.Writer, drain time.Duration) *bridgeHolder {
	hc := &bridgeHolder{
		conn:    conn,
		in:      make(chan proto.Frame, maxBridgeFrames),
		room:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		drained: make(chan struct{}),
		drain:   drain,
	}
	go func() {
		defer close(hc.drained)
		for {
			select {
			case f, ok := <-hc.in:
				if !ok {
					hc.close()
					return
				}
				err := hw.Write(f.Type, f.Payload)
				hc.queued.Add(-int64(len(f.Payload)))
				select {
				case hc.room <- struct{}{}:
				default:
				}
				if err != nil {
					hc.close()
					return
				}
			case <-hc.done:
				return
			}
		}
	}()
	return hc
}

func (hc *bridgeHolder) send(f proto.Frame, bridgeDone <-chan struct{}) bool {
	n := int64(len(f.Payload))
	for q := hc.queued.Load(); q > 0 && q+n > maxBridgeQueued || len(hc.in) == maxBridgeFrames; q = hc.queued.Load() {
		select {
		case <-hc.room:
		case <-hc.done:
			return false
		case <-bridgeDone:
			return false
		}
	}
	hc.queued.Add(n)
	select {
	case hc.in <- f:
		return true
	case <-hc.done:
		return false
	case <-bridgeDone:
		return false
	}
}

func (hc *bridgeHolder) closeSoon() {
	hc.soon.Do(func() { time.AfterFunc(hc.drain, hc.close) })
}

func (hc *bridgeHolder) end() {
	hc.endOnce.Do(func() {
		close(hc.in)
		hc.closeSoon()
	})
}

func (hc *bridgeHolder) close() {
	hc.once.Do(func() {
		close(hc.done)
		_ = hc.conn.Close()
	})
}

func closeWatch(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
}

// shutSoon ends the bridge after bridgeDrain once its input peer has closed,
// so frames still buffered in stdin can pass first on a healthy session.
func (b *bridge) shutSoon() {
	b.soonOnce.Do(func() {
		b.mu.Lock()
		if !b.ended {
			b.soonTimer = time.AfterFunc(b.drain, b.shut)
		}
		b.mu.Unlock()
	})
}

// stopSoon cancels any pending shutSoon timer when runBridge returns cleanly.
func (b *bridge) stopSoon() {
	b.doneOnce.Do(func() { close(b.done) })
	b.mu.Lock()
	b.ended = true
	t := b.soonTimer
	b.soonTimer = nil
	b.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// shut ends the bridge at once when the client is gone: it wakes any wait on
// the holder queue, closes the holder socket, and unblocks stdio reads or
// writes that support deadlines.
func (b *bridge) shut() {
	b.doneOnce.Do(func() {
		close(b.done)
		b.mu.Lock()
		b.ended = true
		b.mu.Unlock()
		b.closeHolder()
		now := time.Now()
		if rd, ok := b.in.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = rd.SetReadDeadline(now)
		}
		if wd, ok := b.out.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = wd.SetWriteDeadline(now)
		}
	})
}
