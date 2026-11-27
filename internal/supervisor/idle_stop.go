package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

type idleStopStateV1 struct {
	state     host.IdleState
	policy    string
	reason    string
	requested bool
	clients   map[*client]struct{}
}

// idleStopTick borrows the current controller's membership and uses the same
// stop path as an explicit stop. It cannot publish stopped or prove an exit.
// An absent setting remains off until the P3.8 wake measurements change it.
func (s *Supervisor) idleStopTick(at time.Time) {
	if s.primary != nil || s.isIdlePrepareBootV1() {
		return
	}
	s.mu.Lock()
	if s.m == nil || s.m.Wall == nil || s.m.Wall.Level != "microvm" || s.m.Policy == nil ||
		s.m.Policy.Sandbox.IdleStop == nil || s.generation == "" || s.rec.phase != host.LifeRunning || s.stopIntent {
		s.idle.state, s.idle.policy = host.IdleState{}, ""
		s.mu.Unlock()
		return
	}
	setting := *s.m.Policy.Sandbox.IdleStop
	limit, err := time.ParseDuration(setting)
	if setting == "never" || err != nil || limit <= 0 {
		s.idle.state, s.idle.policy = host.IdleState{}, setting
		s.mu.Unlock()
		return
	}
	if setting != s.idle.policy {
		s.idle.state, s.idle.policy = host.IdleState{}, setting
	}
	generation := s.generation
	s.mu.Unlock()
	busy := s.sandboxBusyForIdleV1()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.rec.phase != host.LifeRunning || s.stopIntent || s.idle.policy != setting {
		return
	}
	var stop bool
	s.idle.state, stop = host.IdleDecide(at, s.idle.state, generation, busy, limit)
	if stop {
		select {
		case s.stop <- struct{}{}:
			s.idle.reason = "idle_stop"
			s.idle.requested = true
		default:
		}
	}
}

// requestTrustedStopV1 records intent before an outside owner may stop the
// native unit. A concurrent manager SIGTERM then keeps the explicit stop's
// meaning. Only owned runtime cleanup can later establish its stopped state.
func (s *Supervisor) requestTrustedStopV1() {
	root := s.owner()
	root.markStopIntent()
	root.mu.Lock()
	root.idle.requested = true
	if root.idle.reason == "" {
		root.idle.reason = "stopped by request"
	}
	root.mu.Unlock()
}

func (s *Supervisor) signalTrustedStopV1() {
	root := s.owner()
	select {
	case root.stop <- struct{}{}:
	default:
	}
}

func (s *Supervisor) requestedEndV1(kind, reason string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idle.requested {
		return host.EndStop, s.idle.reason
	}
	return kind, reason
}

// Only the original private boot purpose can identify a temporary preparation.
// These methods are supplied by H10 on its retained operation and boot owner.
func (s *Supervisor) isIdlePrepareBootV1() bool {
	type preparePurpose interface{ IsSandboxPrepareBootV1() bool }
	if s.bootOperation != nil {
		if owner, ok := any(s.bootOperation).(preparePurpose); ok && owner.IsSandboxPrepareBootV1() {
			return true
		}
	}
	if s.bootOwner != nil {
		if owner, ok := any(s.bootOwner).(preparePurpose); ok && owner.IsSandboxPrepareBootV1() {
			return true
		}
	}
	return false
}

func (s *Supervisor) sandboxBusyForIdleV1() bool {
	if !s.spawnMu.TryLock() {
		return true
	}
	defer s.spawnMu.Unlock()
	s.membersMu.Lock()
	members := []*Supervisor{s}
	for _, member := range s.members {
		members = append(members, member)
	}
	// A pending member creation or failed launch must not become idle evidence.
	busy := s.memberFault || len(s.expected) > len(s.launches)
	s.membersMu.Unlock()
	for _, member := range members {
		member.mu.Lock()
		busy = busy || member.m == nil || member.rec.phase == host.LifeBooting ||
			len(member.idle.clients) > 0 || member.editorSSHCount > 0
		if member.m != nil {
			busy = busy || member.m.Status.State == host.StateWorking || member.m.Status.State == host.StateStarting
			busy = busy || member.m.Guest != nil && member.m.Guest.State == host.StateWorking && !member.end.claimed
		}
		member.mu.Unlock()
	}
	return busy
}

func (s *Supervisor) stopReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idle.reason != "" {
		return s.idle.reason
	}
	return "stopped by request"
}
