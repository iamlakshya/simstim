package supervisor

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
)

func assertStoppedLeaseSnapshot(t *testing.T, f *leaseFailureFixture) {
	t.Helper()
	saved := f.saved(t)
	if saved.Armed || len(saved.Layers) != 1 || len(saved.Layers[0].Rules) != 0 || saved.Policy == nil || len(saved.Policy.Rules) != 0 {
		t.Fatalf("stopped lease snapshot retained permission or armed state: %+v", saved)
	}
	f.s.mu.Lock()
	armed, dirty := f.s.m.Armed, f.s.dirty
	f.s.mu.Unlock()
	if armed || !dirty {
		t.Fatal("stopped lease snapshot was not disarmed and marked for persistence")
	}
}

func TestAlreadyClosedControlDisarmsTheLeaseSnapshot(t *testing.T) {
	rule := failureRule()
	f := leaseFailureController(t, rule)
	f.s.audit.writeNet(egress.AuditEntry{Host: "example.com", Rule: rule.ID})
	if err := f.peer.Close(); err != nil {
		t.Fatal(err)
	}
	waitEventSignal(t, f.c.done, "control EOF before lease removal")
	waitEventSignal(t, f.apply(rule), "closed control lease snapshot")
	assertStoppedLeaseSnapshot(t, f)
	data, err := os.ReadFile(f.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	netAt := strings.Index(string(data), `"kind":"net"`)
	endAt := strings.Index(string(data), `"event":"rule_end"`)
	if netAt < 0 || endAt < netAt {
		t.Fatalf("control EOF reordered the network audit and lease end: %s", data)
	}
}

func TestDeadOwnerDisarmsTheLeaseSnapshotWhenItsRPCIsSkipped(t *testing.T) {
	rule := failureRule()
	f := leaseFailureController(t, rule)
	// Keep the control readable to isolate the child-exit guard from EOF.
	// The fixture has no lifecycle goroutine that could disarm on our behalf.
	f.c.owner.dead.Store(true)
	waitEventSignal(t, f.apply(rule), "dead owner lease snapshot")
	if channelClosed(f.c.done) {
		t.Fatal("a closed control masked the dead-owner snapshot guard")
	}
	assertStoppedLeaseSnapshot(t, f)
}

func TestControlEOFWhileTheLeaseSnapshotWaitsForStateLockDisarms(t *testing.T) {
	rule := failureRule()
	f := leaseFailureController(t, rule)
	next, removed := removeEndedLeases(f.s.m.Layers, []leaseEnd{{identity: ruleLeaseIdentity(rule)}}, time.Now())
	pol, cleaned, err := egress.MergeLayers(next)
	if err != nil || len(removed) != 1 {
		t.Fatalf("cannot prepare the exact lease removal: %v", err)
	}
	// Hold the state lock across EOF. The caller observes an open control,
	// then its save cannot acquire the lock until that observation is stale.
	f.s.mu.Lock()
	release := sync.OnceFunc(f.s.mu.Unlock)
	t.Cleanup(release)
	observedClosed := channelClosed(f.c.done)
	if observedClosed {
		t.Fatal("control closed before the caller's observation")
	}
	waiting := make(chan struct{})
	saved := make(chan struct{})
	published := make(chan bool, 1)
	f.applied = saved
	go func() {
		defer close(saved)
		close(waiting)
		published <- f.c.saveLeaseState(&pol, cleaned, observedClosed)
	}()
	waitEventSignal(t, waiting, "lease snapshot waiting for the state lock")
	if err := f.peer.Close(); err != nil {
		t.Fatal(err)
	}
	waitEventSignal(t, f.c.done, "control EOF while state lock is held")
	release()
	waitEventSignal(t, saved, "lease snapshot after control EOF")
	if ok := <-published; !ok {
		t.Fatal("current owner lease snapshot was not published")
	}
	assertStoppedLeaseSnapshot(t, f)
}
