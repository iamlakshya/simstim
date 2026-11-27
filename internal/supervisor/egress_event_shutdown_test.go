package supervisor

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func blockedEventAudit(t *testing.T, f *eventFixture) func() {
	t.Helper()
	f.s.audit.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(f.s.audit.mu.Unlock) }
	t.Cleanup(unlock)
	f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "first.example"}})
	waitFor(t, "worker taking blocked audit", func() bool {
		f.c.events.mu.Lock()
		defer f.c.events.mu.Unlock()
		return f.c.events.auditSeq == 1 && len(f.c.events.audits) == 0
	})
	return unlock
}

func TestAuditOverflowFailsControlAndDrainsReceivedEnds(t *testing.T) {
	rule := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", GrantID: "identity", Once: true}
	rule.ID = api.RuleID(rule)
	f := eventController(t, rule)
	f.s.mu.Lock()
	f.s.m.Armed = true
	f.s.mu.Unlock()
	unlock := blockedEventAudit(t, f)
	f.event(t, onceEvent(rule))
	waitFor(t, "received end", func() bool { f.c.events.mu.Lock(); defer f.c.events.mu.Unlock(); return len(f.c.events.ends) == 1 })
	requestDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		requestDone <- f.c.request(ctx, "probe", nil, nil)
	}()
	_ = f.next(t) // No reply: failure of the reader must release this waiter.
	for range maxQueuedNetAudits {
		f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com"}})
	}
	// The overflow frame may race the link close after it was received.
	_ = f.writer.WriteJSON(proto.TEvent, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com"}})
	waitEventSignal(t, f.c.done, "audit overflow")
	waitEventSignal(t, f.c.events.controlClosed, "failed control close")
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("overflow reported a successful unanswered RPC")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("overflow did not release the RPC waiter")
	}
	f.s.mu.Lock()
	armed := f.s.m.Armed
	f.s.mu.Unlock()
	if armed {
		t.Fatal("overflow left networking armed")
	}
	f.c.events.mu.Lock()
	bounded := len(f.c.events.audits) <= maxQueuedNetAudits && f.c.events.auditBytes <= maxQueuedAuditBytes
	retained := f.c.events.emergency != nil && len(f.c.events.ends) == 1
	f.c.events.mu.Unlock()
	if !bounded || !retained {
		t.Fatal("overflow exceeded memory bounds or dropped a received event")
	}
	select {
	case <-f.c.events.finished:
		t.Fatal("worker closed before its blocked audit drained")
	default:
	}
	unlock()
	waitEventSignal(t, f.c.events.finished, "overflow drain")
	f.s.mu.Lock()
	remaining := len(f.s.m.Layers[0].Rules)
	f.s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("overflow lost a legitimate received end")
	}
	b, err := os.ReadFile(f.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), `"kind":"net"`); got != maxQueuedNetAudits+2 {
		t.Fatalf("drained %d audits, want %d", got, maxQueuedNetAudits+2)
	}
	// Only this lease's preceding audit is its barrier. Unrelated audits
	// received after the end may legitimately be written afterwards.
	if !strings.Contains(string(b), "audit_queue_overflow") || strings.Index(string(b), `"host":"first.example"`) > strings.Index(string(b), `"event":"rule_end"`) {
		t.Fatalf("overflow failure or ordering not recorded: %s", b)
	}
}

func TestControlEOFDrainsAuditBeforeTheLeaseEnd(t *testing.T) {
	rule := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", GrantID: "identity", Once: true}
	rule.ID = api.RuleID(rule)
	f := eventController(t, rule)
	unlock := blockedEventAudit(t, f)
	f.event(t, onceEvent(rule))
	waitFor(t, "received end", func() bool { f.c.events.mu.Lock(); defer f.c.events.mu.Unlock(); return len(f.c.events.ends) == 1 })
	_ = f.peer.Close()
	waitEventSignal(t, f.c.done, "control EOF")
	select {
	case <-f.c.events.finished:
		t.Fatal("worker finished before blocked audit drained")
	default:
	}
	unlock()
	waitEventSignal(t, f.c.events.finished, "drained worker")
	f.s.mu.Lock()
	remaining := len(f.s.m.Layers[0].Rules)
	f.s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("received end was lost at EOF")
	}
	b, err := os.ReadFile(f.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	netAt, endAt := strings.Index(string(b), `"kind":"net"`), strings.Index(string(b), `"event":"rule_end"`)
	if netAt < 0 || endAt < netAt {
		t.Fatalf("EOF reordered audit and end: %s", b)
	}
}
