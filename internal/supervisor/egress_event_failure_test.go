package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestEndedLeaseDoesNotRearmAfterPolicyRPCFailure(t *testing.T) {
	rule := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", GrantID: "identity", Once: true}
	rule.ID = api.RuleID(rule)
	f := eventController(t, rule)
	f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com", Rule: rule.ID}})
	f.event(t, onceEvent(rule))
	req := f.next(t)
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: false, Error: &proto.Error{Code: "policy_error", Message: "injected failure", Hint: "Try the policy command again."}}); err != nil {
		t.Fatal(err)
	}
	waitEventSignal(t, f.c.done, "failed policy control")
	waitEventSignal(t, f.c.events.finished, "failed policy drain")
	f.s.mu.Lock()
	remaining := len(f.s.m.Layers[0].Rules)
	f.s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("a spent lease remained after its policy RPC failed")
	}
	b, err := os.ReadFile(f.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"event":"rule_end"`) {
		t.Fatal("spent lease removal was not audited")
	}
}

func TestEgressRequestRetainsItsReplyValue(t *testing.T) {
	f := eventController(t)
	var out map[string]string
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- f.c.request(ctx, "probe", nil, &out)
	}()
	req := f.next(t)
	result, _ := json.Marshal(map[string]string{"identity": "exact"})
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: result}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out["identity"] != "exact" {
		t.Fatalf("reply value changed or was lost: %v", out)
	}
}

func TestOldControlClosesOnlyItsOwnedEgressLink(t *testing.T) {
	f := eventController(t)
	a, b := net.Pipe()
	replacement := link.New(a, link.Supervisor, egress.LinkOptions())
	peer := link.New(b, link.Guest, egress.LinkOptions())
	t.Cleanup(func() { _ = replacement.Close(); _ = peer.Close() })
	f.s.mu.Lock()
	f.s.eg = &egressProc{sess: replacement}
	f.s.m.Armed = true
	f.s.mu.Unlock()
	_ = f.peer.Close()
	waitEventSignal(t, f.c.events.controlClosed, "old owned control close")
	if channelClosed(replacement.Done()) {
		t.Fatal("an old control closed an unrelated replacement link")
	}
	f.s.mu.Lock()
	armed := f.s.m.Armed
	f.s.mu.Unlock()
	if !armed {
		t.Fatal("an old control disarmed an unrelated replacement")
	}
}

func TestLateLeaseEventsCannotRemoveAReplacementGrant(t *testing.T) {
	now := time.Now().UTC()
	old := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", GrantID: "old", Once: true, Until: now.Add(-time.Second)}
	old.ID = api.RuleID(old)
	fresh := old
	fresh.GrantID = "fresh"
	fresh.Once = false
	fresh.Until = time.Time{}
	f := eventController(t, fresh)
	f.event(t, onceEvent(old))
	f.event(t, expiryEvent(old))
	legacy := old
	legacy.GrantID = ""
	f.event(t, onceEvent(legacy))
	// EOF is an ordering barrier: all preceding frames received by the
	// control reader must be drained before the worker reports completion.
	f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "barrier.example"}})
	waitFor(t, "stale event barrier", func() bool { f.c.events.mu.Lock(); defer f.c.events.mu.Unlock(); return f.c.events.auditSeq == 1 })
	_ = f.peer.Close()
	waitEventSignal(t, f.c.events.finished, "stale event drain")
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if len(f.s.m.Layers[0].Rules) != 1 || f.s.m.Layers[0].Rules[0].GrantID != "fresh" || f.s.m.Layers[0].Rules[0].Once {
		t.Fatal("a late or empty-identity event removed the replacement grant")
	}
}

func TestUnconfirmedPolicyUpdateFailsNetworkClosed(t *testing.T) {
	f := eventController(t)
	f.s.mu.Lock()
	f.s.m.Armed = true
	f.s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.c.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetOpen}}, nil)
	}()
	f.next(t)
	// The peer may already have applied the policy. A missing reply cannot
	// leave unknown permissions running behind the old trusted manifest.
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unconfirmed policy update returned %v", err)
	}
	select {
	case <-f.c.done:
	case <-time.After(time.Second):
		t.Fatal("an unconfirmed policy update left network control open")
	}
	waitEventSignal(t, f.c.events.controlClosed, "unconfirmed policy disarm")
	f.s.mu.Lock()
	armed := f.s.m.Armed
	f.s.mu.Unlock()
	if armed || !channelClosed(f.local.Done()) {
		t.Fatal("an unconfirmed policy update retained network access")
	}
}

func TestRejectedPolicyUpdateKeepsExistingControl(t *testing.T) {
	f := eventController(t)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- f.c.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetOpen}}, nil)
	}()
	req := f.next(t)
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: false, Error: &proto.Error{Code: "bad_target", Message: "invalid policy", Hint: "Check the rule target."}}); err != nil {
		t.Fatal(err)
	}
	var rejected *proto.Error
	if err := <-done; !errors.As(err, &rejected) || rejected.Code != "bad_target" {
		t.Fatalf("rejected policy update returned %v", err)
	}
	go func() { done <- f.c.request(context.Background(), "probe", nil, nil) }()
	f.reply(t, f.next(t))
	if err := <-done; err != nil {
		t.Fatalf("explicit rejection closed an otherwise healthy control: %v", err)
	}
}
