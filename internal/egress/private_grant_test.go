package egress

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func privateQuestion(t *testing.T, server *Server, host string, port, waiting int) (*networkHold, []networkHoldTicket) {
	t.Helper()
	server.policyMu.Lock()
	defer server.policyMu.Unlock()
	var tickets []networkHoldTicket
	retained, _, err := server.holds.retain(Request{Host: host, Port: port}, host, server.now(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range waiting {
		ticket, _, err := server.holds.attach(Request{Host: host, Port: port}, host, server.now(), 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
	}
	return retained.entry, tickets
}

func TestPrivateSensitiveOnceUsesActualWaitersAndSameLedger(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
	request := Request{Host: "privatevalue.example.test", Port: 443}
	entry, tickets := privateQuestion(t, server, request.Host, request.Port, 3)
	server.holds.detach(tickets[0])
	ledger, state := server.ledger, server.currentMembers()
	server.policyMu.Lock()
	result, err := server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldOnce, true, "live", request)
	server.policyMu.Unlock()
	if err != nil || !result.Decision.Allow || !api.ValidNetworkHoldID(result.ID) || server.ledger != ledger {
		t.Fatal("real private answer did not install on the original ledger", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", result), request.Host) {
		t.Fatal("private grant result formatting exposed the true target")
	}
	raw, err := json.Marshal(server.currentMembers().metadata.Policy)
	if err != nil || strings.Contains(string(raw), "privatevalue") || server.currentMembers() != state || len(server.pendingNetworkSnapshot().Requests) != 0 {
		t.Fatal("private permission entered public policy or retained an answered question", err)
	}
	engine := server.currentEngine()
	first, second, third := engine.NewPermit(), engine.NewPermit(), engine.NewPermit()
	defer first.Close()
	defer second.Close()
	defer third.Close()
	if !engine.DecideWithPermit(request, first).Allow || !engine.DecideWithPermit(request, second).Allow || engine.DecideWithPermit(request, third).Allow {
		t.Fatal("cancelled waiter invented a lease or live waiters lost their independent once slots")
	}
	if sensitive, classified := first.auditOrigin(); !sensitive || classified {
		t.Fatal("actual private permit lost source-derived display provenance")
	}
	first.Commit()
	second.Commit()
	first.Close()
	second.Close()
	if engine.Decide(request).Allow {
		t.Fatal("committed private once grant was reusable")
	}
}

func TestPrivateSensitiveSessionCarriesThroughRealPolicyAndMemberReplacement(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "codex", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
	request := Request{Host: "privatevalue.example.test", Port: 443}
	entry, _ := privateQuestion(t, server, request.Host, request.Port, 0)
	server.policyMu.Lock()
	result, err := server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldSession, false, "live", request)
	server.policyMu.Unlock()
	if err != nil || !result.Decision.Allow {
		t.Fatal(err)
	}
	oldEngine := server.currentEngine()
	policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "unrelated.example"}}}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine() == oldEngine || !server.currentEngine().Decide(request).Allow {
		t.Fatal("immutable replacement lost the running private session rule")
	}
	if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"SECOND": "another-value"}})); err != nil {
		t.Fatal(err)
	}
	if !server.currentEngine().Decide(request).Allow {
		t.Fatal("same-kind source addition discarded private authority")
	}
	policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Target: "*.example.test"})
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("carried private allowance bypassed a current same-layer deny")
	}
	server.policyMu.Lock()
	removed, revokeErr := server.revokeMemoryGrantLocked(t.Context(), result.ID)
	server.policyMu.Unlock()
	if !removed || revokeErr != nil {
		t.Fatal("opaque private rule ID did not revoke its actual compiled group")
	}
	policy.Rules = policy.Rules[:1]
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("policy replacement revived a revoked private rule")
	}
	cold := newMemberServer(t, InitArgs{Agent: "shell", Policy: policy, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
	if cold.currentEngine().Decide(request).Allow {
		t.Fatal("cold actor reconstructed a private session target")
	}
	_ = server.Close()
	for _, layer := range server.currentEngine().layers {
		for _, row := range layer.rules {
			if row.memoryOnly {
				t.Fatal("stopped actor retained private rule references")
			}
		}
	}
}

func TestPrivateAnswerNeverCreatesNumericExceptionOrBypassesCurrentLock(t *testing.T) {
	private := netip.AddrFrom4([4]byte{10, 23, 0, 1})
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
	entry, _ := privateQuestion(t, server, private.String(), 443, 0)
	before := server.currentEngine()
	server.policyMu.Lock()
	_, err := server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldOnce, false, "live", Request{Host: private.String(), Port: 443})
	server.policyMu.Unlock()
	if err == nil || server.currentEngine() != before || server.currentEngine().AllowsAddr(private, 443) {
		t.Fatal("opaque Own answer fabricated a private numeric exception")
	}
	host := "privatevalue.example.test"
	entry, _ = privateQuestion(t, server, host, 443, 0)
	locked := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk, Locks: []api.Lock{{Layer: api.LayerAll, Deny: []string{"*.example.test"}}}}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: locked})); err != nil {
		t.Fatal(err)
	}
	// Real replacement reconciles a zero-waiter question before a late answer.
	server.policyMu.Lock()
	_, err = server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldSession, true, "live", Request{Host: host, Port: 443})
	server.policyMu.Unlock()
	if err == nil || server.currentEngine().Decide(Request{Host: host, Port: 443}).Allow {
		t.Fatal("late sensitive answer bypassed current lock or ended identity")
	}
}

func TestClassifierLateOnceUsesPrivateCallbackWithoutSessionDurabilityShim(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify}})
	request := Request{Host: "late.example", Port: 443}
	entry, _ := privateQuestion(t, server, request.Host, 443, 0)
	binding := classifierGrantBinding{source: server.currentMembers(), engine: server.currentEngine(), request: request, hold: entry, expires: entry.meta.Expires}
	server.policyMu.Lock()
	_, err := server.installClassifierGrantLocked(t.Context(), binding, classify.Result{Action: "allow", Scope: "session"})
	server.policyMu.Unlock()
	if err == nil || server.currentEngine().Decide(request).Allow {
		t.Fatal("ordinary session was faked as completed memory-only durable publication")
	}
	scope := "once"
	policy := binding.source.metadata.Policy
	policy.Order = []api.LayerRef{{Kind: api.LayerAll}}
	policy.Classifier.MaxScope = &scope
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	entry, _ = privateQuestion(t, server, request.Host, 443, 0)
	binding = classifierGrantBinding{source: server.currentMembers(), engine: server.currentEngine(), request: request, hold: entry, expires: entry.meta.Expires}
	server.policyMu.Lock()
	decision, err := server.installClassifierGrantLocked(t.Context(), binding, classify.Result{Action: "allow", Scope: "session"})
	server.policyMu.Unlock()
	if err != nil || !decision.Allow {
		t.Fatal("actual callback failed to clamp and install a late once grant", err)
	}
	engine := server.currentEngine()
	first, second := engine.NewPermit(), engine.NewPermit()
	defer first.Close()
	defer second.Close()
	if !engine.DecideWithPermit(request, first).Allow || engine.DecideWithPermit(request, second).Allow {
		t.Fatal("late rule was not one actual shared-ledger once slot")
	}
	if _, classified := first.auditOrigin(); !classified {
		t.Fatal("actual classifier permit lost its acquired-rule provenance")
	}
}
