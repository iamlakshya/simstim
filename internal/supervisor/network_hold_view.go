package supervisor

import (
	"context"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// One bounded display snapshot belongs to its existing egressProc owner.
// No target, rule, ledger or positive action is resolved from this cache.
type networkHoldView struct {
	mu       sync.Mutex
	valid    bool
	snapshot api.NetworkHoldSnapshot
}

func (v *networkHoldView) apply(snapshot api.NetworkHoldSnapshot) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.valid && snapshot.Revision < v.snapshot.Revision {
		return
	}
	snapshot.Requests = append([]api.PendingNetworkRequest(nil), snapshot.Requests...)
	v.snapshot, v.valid = snapshot, true
}

func (v *networkHoldView) clear() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.valid = false
	v.snapshot = api.NetworkHoldSnapshot{}
}

func (v *networkHoldView) status() (pending bool, stopped bool, reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.valid {
		return false, false, ""
	}
	if len(v.snapshot.Requests) == 0 {
		return false, v.snapshot.Stopped, ""
	}
	// The supervisor has no current secret redactor. Host labels are served
	// only by fresh egress snapshots, never retained as status reasons.
	return true, v.snapshot.Stopped, "pending network request"
}

func (c *egressCtl) refreshNetworkHolds(ctx context.Context, revision uint64) {
	if channelClosed(c.done) {
		return
	}
	owned, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var snapshot api.NetworkHoldSnapshot
	if err := c.request(owned, egress.CtlNetworkPending, struct{}{}, &snapshot); err != nil || !api.ValidNetworkHoldSnapshot(snapshot) || snapshot.Revision < revision {
		c.closeOwnedControl()
		return
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.owner == nil || c.s.eg != c.owner || c.owner.dead.Load() {
		return
	}
	if c.owner.networkHolds == nil {
		c.owner.networkHolds = new(networkHoldView)
	}
	c.owner.networkHolds.apply(snapshot)
	c.s.showStatus(time.Now())
}

// The caller holds s.mu, as the other trusted status overlays do.
func (s *Supervisor) pendingNetworkStatus() (bool, bool, string) {
	if s.eg == nil || s.eg.networkHolds == nil || s.eg.dead.Load() {
		return false, false, ""
	}
	return s.eg.networkHolds.status()
}
