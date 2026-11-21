package supervisor

import (
	"github.com/iamlakshya/simstim/internal/alerts"
)

func (s *Supervisor) isSandboxPrepareBootV1() bool {
	if s == nil {
		return false
	}
	if s.bootOperation != nil && s.bootOperation.IsSandboxPrepareBootV1() {
		return true
	}
	if s.bootOwner != nil && s.bootOwner.IsSandboxPrepareBootV1() {
		return true
	}
	return false
}

func (s *Supervisor) recordStartedAlertsAndLaunch(restored, primaryReady bool) {
	if s.isSandboxPrepareBootV1() {
		return
	}
	if primaryReady {
		s.raise(s.alertEvent(alerts.SandboxReady))
	}
	kind := alerts.Started
	if restored {
		kind = alerts.Restored
	}
	s.raise(s.alertEvent(kind))
	s.registerLaunch()
}
