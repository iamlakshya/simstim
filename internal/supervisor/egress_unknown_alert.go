package supervisor

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/egress"
)

// The existing serial audit worker calls this with pre-scrubbed display data.
// Failed/cancelled append never claims the destination, so a later accepted
// concurrent audit can still elect its first notification.
func (c *egressCtl) acceptUnknownAudit(ctx context.Context, entry egress.AuditEntry) {
	if err := c.s.audit.appendNet(entry); err != nil {
		if c.s.log != nil {
			c.s.log.Warn("unknown allowance audit was not accepted", "err", err, "try", egressRecoveryHint)
		}
		return
	}
	c.s.mu.Lock()
	current := c.owner != nil && c.s.eg == c.owner && !c.owner.dead.Load()
	c.s.mu.Unlock()
	if !current || channelClosed(c.done) {
		return
	}
	owned, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var result egress.UnknownAuditAcceptedResult
	if err := c.request(owned, egress.CtlUnknownAuditAccepted, egress.UnknownAuditAcceptedArgs{Version: 1, Destination: entry.UnknownDestination}, &result); err != nil {
		// A lost acknowledgement cannot be retried into duplicate native
		// notifications. Retire this exact uncertain control owner; a new
		// cold actor starts with its own empty destination history.
		c.closeOwnedControl()
		return
	}
	if !result.First {
		return
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.eg != c.owner || c.owner.dead.Load() {
		return
	}
	event := c.s.alertEvent(alerts.EgressUnknown)
	event.Reason = entry.Host
	c.s.raise(event)
}
