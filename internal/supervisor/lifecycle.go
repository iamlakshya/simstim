package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// How a run ends. The guest's end of the agent is a claim: it shows as
// claimed, and raises the end's alert, but it does not end the run. The
// memory watch goes on until the sandbox really ends. Every end runs the
// sandbox's one cleanup (closeSandbox), then closes the record (life.go):
// a kill by request, a stop with the host, or an error of the sandbox,
// which raises sandbox_error.

// The graces of a stop: what the guest init gets to stop its holder and
// agent before the supervisor kills the sandbox.
const (
	killGrace   = 3 * time.Second  // a kill, as for rm or resume: the holder's own wait
	systemGrace = 10 * time.Second // a stop with the host, inside the unit's 30 s
	// stopSlack is what the supervisor adds to a grace. The guest init
	// gives the holder the grace and 4 s more: the holder's own wait for
	// its agent, then a second.
	stopSlack = 5 * time.Second
)

// endFacts are what the guest's claims did about the agent's end. They
// are no facts of the run: the record holds those.
type endFacts struct {
	afterStop bool   // physical end after controller-issued Stop/System
	token     string // current controller-run token of the correlated end
	claimed   bool   // the guest claimed the agent's end
	alerted   bool   // the end's alert went out
}

// wait waits until a signal, a kill, the sandbox's end or the link's end
// stops the session. Each end then runs the sandbox's one cleanup, and
// closes the record.
func (s *Supervisor) wait() {
	ended := make(chan struct{})
	go func() {
		_ = s.sb.Wait()
		close(ended)
	}()
	var kind, reason string
	natural := false
	select {
	case <-s.lifetimeContext().Done():
		s.markStopIntent()
		s.ending()
		s.stopGuest(systemGrace, ended)
		kind, reason = host.EndSystem, "supervisor lifetime ended"
	case sg := <-s.sig:
		s.markStopIntent()
		// The system, or the user's manager, stops the session, as at a
		// shutdown. The session stays eligible for restore.
		s.log.Info("stopping on a signal", "signal", sg.String())
		s.ending()
		s.stopGuest(systemGrace, ended)
		kind, reason = host.EndSystem, "stopped with the host"
	case <-s.stop:
		s.markStopIntent()
		s.ending()
		s.stopGuest(systemGrace, ended)
		kind, reason = host.EndStop, "stopped by request"
	case reason = <-s.quit:
		if s.diskStopReason() != nil {
			s.markStopIntent()
		}
		s.ending()
		s.stopGuest(killGrace, ended)
		kind = host.EndKill
	case <-ended:
		natural = true
		s.audit.write("sandbox_ended", nil)
		s.ending()
		kind, reason = host.EndSandbox, "the sandbox ended"
	case <-s.link.Done():
		if s.ctl != nil {
			select {
			case <-s.ctl.done:
			case <-time.After(250 * time.Millisecond):
			}
		}
		if s.captureWillClose() || s.correlatedEmpty() {
			select {
			case <-ended:
				natural = true
			case <-time.After(stopSlack):
			}
		}
		reason = s.lostReason()
		s.log.Warn("the link closed", "why", reason, "err", s.link.Err())
		s.audit.write("link_closed", map[string]any{"reason": reason})
		s.ending()
		kind = host.EndSandbox
	}
	if disk := s.diskStopReason(); disk != nil {
		kind, reason = host.EndSandbox, disk.Error()
		s.raiseDiskFailure()
	}
	cleanupErr := s.closeSandbox()
	// The owned exit can win the select before its final ctl event is read.
	// Drain that reader before deciding whether a capture is ready.
	if s.ctl != nil {
		select {
		case <-s.ctl.done:
		case <-time.After(time.Second):
		}
	}
	if s.naturalEmpty(natural, cleanupErr) {
		kind, reason = host.EndEmpty, "all sessions ended"
	}
	if err := s.commitCapture(kind, natural, cleanupErr); err != nil && kind == host.EndEmpty {
		kind, reason = host.EndSandbox, "state capture failed"
	}
	if err := s.acceptVMCapture(kind, natural, cleanupErr); err != nil && kind == host.EndEmpty {
		kind, reason = host.EndSandbox, "state capture failed"
	}
	s.finish(kind, reason)
}

// ending moves the record to its last phase.
func (s *Supervisor) ending() {
	s.mu.Lock()
	s.setPhase(host.LifeEnding)
	s.mu.Unlock()
}

// stopGuest asks the guest init to stop within grace, and waits for the
// sandbox's end, or for the grace and stopSlack.
func (s *Supervisor) stopGuest(grace time.Duration, ended <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := s.ctl.request(ctx, proto.CtlStop, proto.CtlStopArgs{GraceMS: int(grace / time.Millisecond)}, nil)
	cancel()
	if err != nil {
		if fallback, ok := s.sb.(interface{ PowerButton(context.Context) error }); ok {
			power, cancel := context.WithTimeout(context.Background(), time.Second)
			err = fallback.PowerButton(power)
			cancel()
		}
	}
	if err == nil {
		select {
		case <-ended:
		case <-time.After(grace + stopSlack):
		}
	}
}

// closeSandbox is the sandbox's one cleanup, on every end: its driver
// kills each process that it can tie to the sandbox, and waits for the
// sandbox's end. It may run more than once.
func (s *Supervisor) closeSandbox() (cleanupErr error) {
	defer func() {
		s.stopEgress()
		s.runtimeCleanup.Do(func() {
			s.mu.Lock()
			s.runtimeCleanupErr = cleanupErr
			s.mu.Unlock()
			if s.runtimeCleaned != nil {
				close(s.runtimeCleaned)
			}
		})
	}()
	if s.diskStopReason() != nil {
		s.markStopIntent()
	}
	cleanupErr = s.stopDiskWatcherV1()
	s.mu.Lock()
	runtime, attempted := s.sb, s.sandboxStartAttempted
	s.mu.Unlock()
	if runtime != nil && attempted {
		killErr := runtime.Kill()
		waitErr := runtime.Wait()
		cleanupErr = errors.Join(cleanupErr, killErr, waitErr)
	}
	// An unstarted factory owns only prepared leases and metadata pins.
	// Release those non-destructively, after any watcher has joined.
	return errors.Join(cleanupErr, s.closeDiskResourcesV1())
}

// finish closes the record with the run's end, makes the session's last
// save, closes its socket and its alerts, and waits for the supervisor's
// own goroutines.
func (s *Supervisor) finish(kind, reason string) {
	// A guest may have exited just after flushing its terminal event. Let
	// the ctl reader apply those bytes before the record becomes final.
	if s.ctl != nil {
		select {
		case <-s.ctl.done:
		case <-time.After(250 * time.Millisecond):
		}
	}
	_ = s.link.Close()
	s.mu.Lock()
	s.closeRecord(kind, reason)
	m := s.snapshot()
	s.mu.Unlock()
	s.log.Info("supervisor exiting", "reason", m.Status.Reason)
	s.audit.write("stop", map[string]any{"kind": kind, "reason": reason})
	// The alerts raised so far go out, the end's and sandbox_error too.
	s.al.Settle(2 * time.Second)
	s.al.Close(2 * time.Second)
	_ = s.save(&m, true)
	s.closeSocket()
	s.finishMembers(kind, reason)
	s.wg.Wait()
}

// raiseError raises sandbox_error, and the end's alert when the guest never
// claimed one. The caller holds s.mu.
func (s *Supervisor) raiseError() {
	s.raise(s.alertEvent(alerts.SandboxError))
	if !s.end.alerted {
		s.end.alerted = true
		s.raise(s.alertEvent(alerts.Failed))
	}
}

// claimNote is what the trusted end says of an end that the guest claimed
// before, or "". The caller holds s.mu.
func (s *Supervisor) claimNote() string {
	g := s.m.Guest
	if g == nil || g.ExitCode == nil {
		return ""
	}
	if g.Signal != "" {
		return " (after a claimed end: " + g.Signal + ")"
	}
	return fmt.Sprintf(" (after a claimed end: exit code %d)", *g.ExitCode)
}

// failed records that the session did not start, with the end of kind,
// raises sandbox_error for a failure, makes the last save, closes the
// socket, and returns err. The save comes before the socket closes: New
// waits for one or the other.
func (s *Supervisor) failed(kind string, err error) error {
	s.raiseDiskFailure()
	if s.capture != nil {
		defer s.capture.Close()
	}
	s.log.Error("the session did not start", "err", err)
	fields := map[string]any{"reason": err.Error()}
	var gs *guestError
	if errors.As(err, &gs) {
		// The guest's own words, cleaned and cut short, and marked as its.
		fields = map[string]any{"code": gs.pe.Code, "guest_says": gs.text}
	}
	s.audit.write("start_failed", fields)
	s.mu.Lock()
	s.closeRecord(kind, err.Error())
	m := s.snapshot()
	s.mu.Unlock()
	s.finishMembers(kind, err.Error())
	s.al.Settle(2 * time.Second)
	s.al.Close(2 * time.Second)
	_ = s.save(&m, true)
	s.closeSocket()
	return err
}

// guestError is an error that the guest init sent: its own words, cleaned,
// as the message of a trusted error.
type guestError struct {
	pe   *proto.Error
	text string
}

func (e *guestError) Error() string { return e.pe.Error() }

// Unwrap gives the trusted error, with its code and hint.
func (e *guestError) Unwrap() error { return e.pe }

// claimedError is an error that the guest init sent, cleaned, with a hint
// from the trusted side: the guest's own hint is a claim too.
func claimedError(pe *proto.Error) error {
	text := cleanClaim(pe.Message, maxReason)
	if pe.Code == "secret_store" {
		return &guestError{text: text, pe: &proto.Error{Code: "secret_store", Message: "the sandbox says: " + text, Hint: "Check the agent's stored-login setup. A failed or uncertain Store needs a fresh sandbox. Explicit exposure works only for tools that read their credential environment variable."}}
	}
	return &guestError{text: text, pe: &proto.Error{Code: "start_failed", Message: "the sandbox says: " + text,
		Hint: "Check the agent's command, then resume the session."}}
}

// linkSilent is how long the guest init may stay silent (spec 10.3).
const linkSilent = 15 * time.Second

// watchBeat raises link_silent, with its audit line, once the guest init
// has sent no valid heartbeat for linkSilent, and again after each new
// silence. Other events do not count.
func (s *Supervisor) watchBeat() {
	s.mu.Lock()
	defer s.mu.Unlock()
	quiet := now().Sub(s.lastBeat) > linkSilent
	first := quiet && !s.silent
	s.silent = quiet
	if first {
		s.captureFailed = true
		s.log.Warn("the guest init is silent", "for", linkSilent)
		s.audit.write("link_silent", nil)
		s.raise(s.alertEvent(alerts.LinkSilent))
	}
}

// loseLink closes the link because of why: a breach of its rules, or a
// ctl stream that ended. The session then ends with sandbox_error.
func (s *Supervisor) loseLink(why string) {
	s.mu.Lock()
	if s.lost == "" {
		s.lost = why
	}
	s.mu.Unlock()
	_ = s.link.Close()
}

// lostReason is why the link ended, for the session's reason.
func (s *Supervisor) lostReason() string {
	s.mu.Lock()
	why := s.lost
	s.mu.Unlock()
	switch err := s.link.Err(); {
	case why != "":
		return why
	case errors.Is(err, link.ErrBreach):
		return "the sandbox broke the link's rules"
	case errors.Is(err, link.ErrNoHello):
		return "the sandbox's guest init did not answer"
	}
	return "the sandbox's link closed"
}

// wallError is an error of the sandbox's start, with its hint. err may be
// nil.
func wallError(msg string, err error) error {
	if err != nil {
		msg += ": " + err.Error()
	}
	return &proto.Error{Code: "wall_setup", Message: msg,
		Hint: "Read the session's supervisor.log, then resume the session."}
}

// isKill reports whether sig, as a holder wrote it, is SIGKILL: the signal
// of the kernel's out-of-memory killer.
func isKill(sig string) bool {
	switch strings.TrimPrefix(strings.ToUpper(sig), "SIG") {
	case "KILLED", "KILL":
		return true
	}
	return false
}
