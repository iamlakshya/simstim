package client

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// BridgeIdleExit is how long a bridge started over a KeepAliver transport
// waits for the client's pings before it ends itself.
const BridgeIdleExit = 30 * time.Second

// leasePeriod is how often a connected client renews a sandbox's lease.
var leasePeriod = time.Minute

// keepAlive pings the bridge every d while c is open, so that a bridge which
// would outlive a lost client (see KeepAliver) knows the client is there.
func (c *Conn) keepAlive(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if c.Ping() != nil {
				return
			}
		}
	}
}

// keepLease renews the lease of c's machine every period while c is open:
// long while any session is working, short while none is. When the laptop
// sleeps or closes, nobody renews it and the machine pauses by itself.
func keepLease(ctx context.Context, c *Conn, l Leaser, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		busy, idle := l.Leases()
		d := idle
		if sessions, err := c.Sessions(ctx); err != nil || anyWorking(sessions) {
			// When the host cannot say, keep the longer lease: an agent
			// frozen mid-turn costs more than a few idle minutes.
			d = busy
		}
		_ = c.renewActiveLease(ctx, l, d, busy)
		cancel()
		select {
		case <-c.done:
			return
		case <-t.C:
		}
	}
}

// anyWorking reports whether a session is starting or working.
func anyWorking(sessions []Session) bool {
	for _, s := range sessions {
		if s.State == api.StateWorking || s.State == api.StateStarting {
			return true
		}
	}
	return false
}

// renewActiveLease serialises the real provider renewal with stream admission.
func (c *Conn) renewActiveLease(ctx context.Context, l Leaser, d, busy time.Duration) error {
	c.leaseMu.Lock()
	defer c.leaseMu.Unlock()
	if c.execBusy.Load() > 0 {
		d = busy
	}
	return l.Renew(ctx, d)
}
