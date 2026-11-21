package supervisor

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/egress"
)

// Only the serial audit worker calls this, after recording the refusal. It
// never runs in the reply reader or sends model bodies, paths or tool values
// to a configured destination. Existing dispatcher noise limits still apply.
func (s *Supervisor) raiseModelBlocked(entry egress.AuditEntry) {
	if entry.Rule == "model:budget" {
		return
	} // the meter raises its own once-only event
	if entry.Decision != "deny" || (!strings.HasPrefix(entry.Rule, "model:") && !strings.HasPrefix(entry.Rule, "service:")) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		s.raise(s.alertEvent(alerts.EgressBlocked))
	}
}
