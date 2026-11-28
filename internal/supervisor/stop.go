package supervisor

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
)

type stopKind int

const (
	stopKindKill stopKind = iota
	stopKindStop
	stopKindHostStop
	stopKindDiskFull
	stopKindIdleStop
)

type stopRequest struct {
	kind   stopKind
	reason string
	system bool
}

func (s *Supervisor) requestStop(reason string, system bool) {
	s.requestStopKind(stopKindKill, reason, system)
}

func (s *Supervisor) requestStopKind(kind stopKind, reason string, system bool) {
	s.stopOnce.Do(func() {
		s.stopCh <- stopRequest{kind: kind, reason: reason, system: system}
	})
}

// finishStopReq shuts down the sandbox cleanly (guest ctl stop, then driver
// Stop such as Cloud Hypervisor ACPI power-button, then Kill fallback), closes
// the egress process and sockets, removes temporary VMM/runtime files via
// Destroy (keeping disk.img and state), and updates the trusted manifest.
// Stop keeps no screen and makes no memory snapshot (questions 10 and 11).
func (s *Supervisor) finishStopReq(sr stopRequest) {
	close(s.doneCh)
	s.closeAlerts()
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()

	deadline := time.Now().Add(s.opt.StopGrace)
	ctlCtx, ctlCancel := context.WithDeadline(context.Background(), deadline)
	_, ctlErr := s.ctlCall(ctlCtx, "stop", guest.StopReq{GraceMS: int(s.opt.StopGrace / time.Millisecond)})
	ctlCancel()

	// If ctl "stop" was acknowledged, wall-init is already running its own
	// shutdown with StopGrace plus keep-file persistence; give it that window
	// plus a short buffer before escalating with Stop.
	if ctlErr == nil {
		waitCh := make(chan struct{})
		go func() {
			_ = s.sb.Wait()
			close(waitCh)
		}()
		rem := time.Until(deadline) + 2*time.Second
		if rem > 0 {
			t := time.NewTimer(rem)
			select {
			case <-waitCh:
				t.Stop()
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.sb.Stop(ctx, 2*time.Second)
				cancel()
				<-waitCh
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.sb.Stop(ctx, 2*time.Second)
			cancel()
			<-waitCh
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), s.opt.StopGrace)
		_ = s.sb.Stop(ctx, s.opt.StopGrace)
		cancel()
	}

	s.closeAllPorts()
	s.closeAllConns()
	_ = s.link.Close()
	s.wg.Wait()
	s.closeEgress()
	_ = s.sb.Destroy()

	now := time.Now().UTC()
	s.mu.Lock()
	_ = s.mem.Reap(s.id)
	s.m.GrantedPorts = nil
	s.ended = true
	applyStopToManifest(s.m, sr, now)
	childCopies := s.finalizeChildrenLocked(sr, now)
	s.mu.Unlock()
	_ = s.save(true)
	for _, cc := range childCopies {
		_ = cc.Save()
	}

	auditEvent := "kill"
	switch sr.kind {
	case stopKindKill:
		auditEvent = "kill"
	case stopKindStop:
		auditEvent = "stop"
	case stopKindHostStop:
		auditEvent = "host_stop"
	case stopKindIdleStop:
		auditEvent = "idle_stop"
	case stopKindDiskFull:
		auditEvent = "disk_full_stop"
	}
	s.audit.Record(AuditEntry{Event: auditEvent, Session: s.id, Detail: sr.reason})
	// Close the session sockets last so callers polling alive(id) (such as
	// resume, stop or delete) wait until sb.Destroy and the final manifest saves finish.
	s.closeChildSockets()
	s.closeSocket()
	<-s.acceptDone
}

// applyStopToManifest updates a primary or child session manifest according to
// sr so child sessions mirror the primary session's state and cleared runtime/
// guest fields across host_stop, stop/idle_stop/disk_full, and kill.
func applyStopToManifest(m *host.Manifest, sr stopRequest, now time.Time) {
	switch sr.kind {
	case stopKindHostStop:
		// Host shutdown (SIGTERM/SIGINT/SIGHUP): preserve Status.State so
		// host.Restore(false) after reboot restores active sessions and lazily
		// transitions idle microvms to StateStopped (spec 6.6).
		m.Run.HolderPID = 0
		m.Run.AgentPID = 0
		m.Run.Unit = ""
		m.Guest = nil
		m.Status.Reason = sr.reason
		m.Status.Source = "system"
		m.Status.Claimed = false
	case stopKindStop, stopKindDiskFull, stopKindIdleStop:
		src := "user"
		if sr.system {
			src = "system"
		}
		m.Run.HolderPID = 0
		m.Run.AgentPID = 0
		m.Run.Unit = ""
		m.Run.RespawnCount = 0
		m.Guest = nil
		m.Status = host.Status{
			State:   host.StateStopped,
			Since:   now,
			Source:  src,
			Reason:  sr.reason,
			Claimed: false,
		}
	default:
		switch {
		case sr.system:
			m.Status.Reason, m.Status.Source, m.Status.Claimed = sr.reason, "system", false
		case m.Status.State != host.StateExited && m.Status.State != host.StateErrored:
			m.Status = host.Status{State: host.StateExited, Since: now, Reason: sr.reason, Claimed: false}
		default:
			m.Status.Claimed = false
		}
	}
}

// checkIdleStop enforces [sandbox] idle_stop for this session using
// host.SessionIdleDecide (isolation spec 6.6, question 9). Off by default
// until configured.
func (s *Supervisor) checkIdleStop(now time.Time) {
	after := s.opt.IdleStop
	s.mu.Lock()
	if s.ended || s.stopping {
		s.mu.Unlock()
		return
	}
	if after <= 0 && s.m.Wall != nil && s.m.Wall.IdleStop != "" {
		if d, err := host.ParseIdleStop(s.m.Wall.IdleStop); err == nil {
			after = d
		}
	}
	if after <= 0 {
		s.mu.Unlock()
		return
	}
	state := s.m.Status.State
	working := state == host.StateWorking || state == host.StateStarting
	if !working {
		for _, sess := range s.sessions {
			if sess != nil && sess.m != nil && !sess.ended {
				st := sess.m.Status.State
				if st == host.StateWorking || st == host.StateStarting {
					working = true
					break
				}
			}
		}
	}
	busy := s.attached.Load() > 0 || working
	nextBusyAt, shouldStop := host.SessionIdleDecide(now, s.idleBusyAt, busy, after)
	s.idleBusyAt = nextBusyAt
	s.mu.Unlock()

	if shouldStop {
		reason := "idle for " + after.String()
		s.log.Info("stopping sandbox: idle_stop", "after", after.String())
		s.requestStopKind(stopKindIdleStop, reason, true)
	}
}
