package supervisor

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func idleSandboxV1() *Supervisor {
	return &Supervisor{id: "a2a2a2a2", generation: "current-run", stop: make(chan struct{}, 1), rec: record{phase: host.LifeRunning},
		m: &host.Manifest{Wall: &host.WallRecord{Level: "microvm", Driver: "chv"}, Policy: &api.Policy{}, Status: host.Status{State: host.StateIdle}}}
}

func TestSandboxIdleStopIsOffUntilExplicitlyConfigured(t *testing.T) {
	for _, setting := range []*string{nil, new("never"), new(""), new("0s"), new("bad")} {
		s := idleSandboxV1()
		s.m.Policy.Sandbox.IdleStop = setting
		s.idleStopTick(time.Unix(1, 0))
		s.idleStopTick(time.Unix(86401, 0))
		if len(s.stop) != 0 || s.rec.phase != host.LifeRunning {
			t.Fatal("an absent or disabled idle time stopped the sandbox")
		}
	}
}

func TestSandboxIdleStopCountsEveryClientAndMember(t *testing.T) {
	s := idleSandboxV1()
	s.m.Policy.Sandbox.IdleStop = new("1m")
	child := idleSandboxV1()
	child.id, child.primary = "b3b3b3b3", s
	s.members = map[string]*Supervisor{child.id: child}
	first, last := &client{}, &client{}
	child.noteAttach(first, proto.Attach{})
	child.noteAttach(last, proto.Attach{})
	child.drop(last) // losing the size leader must not forget the first client
	at := time.Unix(100, 0)
	s.idleStopTick(at)
	s.idleStopTick(at.Add(2 * time.Minute))
	if len(s.stop) != 0 {
		t.Fatal("the VM stopped with a client attached to another session")
	}
	child.drop(first)
	child.m.Status.State = host.StateWorking
	s.idleStopTick(at.Add(3 * time.Minute))
	child.m.Status.State = host.StateIdle
	s.idleStopTick(at.Add(3*time.Minute + 59*time.Second))
	if len(s.stop) != 0 {
		t.Fatal("the idle period included a working member")
	}
	s.idleStopTick(at.Add(4 * time.Minute))
	if len(s.stop) != 1 || s.stopReason() != "idle_stop" || s.rec.phase != host.LifeRunning || s.stopIntent {
		t.Fatal("idle expiry did not request the same stop without inventing exit evidence")
	}
}

func TestSandboxIdleStopDoesNotCarryTimeAcrossRunOrPolicyChanges(t *testing.T) {
	s := idleSandboxV1()
	s.m.Policy.Sandbox.IdleStop = new("1m")
	at := time.Unix(100, 0)
	s.idleStopTick(at)
	s.generation = "next-run"
	s.idleStopTick(at.Add(time.Hour))
	if len(s.stop) != 0 {
		t.Fatal("an old run expired the new run")
	}
	s.m.Policy.Sandbox.IdleStop = new("2m")
	s.idleStopTick(at.Add(2 * time.Hour))
	s.idleStopTick(at.Add(2*time.Hour + time.Minute))
	if len(s.stop) != 0 {
		t.Fatal("an earlier policy expired the new idle duration")
	}
	s.idleStopTick(at.Add(2*time.Hour + 2*time.Minute))
	if len(s.stop) != 1 {
		t.Fatal("the new idle policy never expired")
	}
}

func TestSandboxIdleStopDoesNotCountAnAbsentPolicyInterval(t *testing.T) {
	s := idleSandboxV1()
	s.m.Policy.Sandbox.IdleStop = new("1m")
	at := time.Unix(100, 0)
	s.idleStopTick(at)
	s.m.Policy.Sandbox.IdleStop = nil
	s.idleStopTick(at.Add(time.Hour))
	s.m.Policy.Sandbox.IdleStop = new("1m")
	s.idleStopTick(at.Add(2 * time.Hour))
	if len(s.stop) != 0 {
		t.Fatal("the time with idle stop off expired a re-enabled policy")
	}
}

func TestSandboxIdleStopCannotMistakeMemoryWarningForIdleWork(t *testing.T) {
	s := idleSandboxV1()
	s.m.Policy.Sandbox.IdleStop = new("1m")
	s.m.Status.State, s.m.Status.Source = host.StateNeedsInput, host.SrcMemory
	s.m.Guest = &host.GuestClaims{State: host.StateWorking}
	at := time.Unix(100, 0)
	s.idleStopTick(at)
	s.idleStopTick(at.Add(time.Hour))
	if len(s.stop) != 0 {
		t.Fatal("a memory warning hid a working guest from the idle decision")
	}
}

func TestExplicitStopIntentSurvivesUnitSignalWithoutProvingExit(t *testing.T) {
	s := idleSandboxV1()
	s.requestTrustedStopV1()
	if !s.stopIntent || len(s.stop) != 0 || s.rec.phase != host.LifeRunning || s.rec.end != nil {
		t.Fatal("recording stop intent either queued before the ACK or invented exit")
	}
	kind, why := s.requestedEndV1(host.EndSystem, "manager SIGTERM")
	if kind != host.EndStop || why != "stopped by request" {
		t.Fatal("the native unit's signal relabelled an explicit stop")
	}
	s.signalTrustedStopV1()
	if len(s.stop) != 1 || s.rec.phase != host.LifeRunning {
		t.Fatal("the acknowledged stop did not reach the existing wait path")
	}
}
