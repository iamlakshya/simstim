package supervisor

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestLockdownCASPreservesAConcurrentProtectedPolicyMutation(t *testing.T) {
	f, _ := replacementFixture(t)
	previous, _ := api.HashPolicyLayerVectorV1(f.s.m.Layers)
	f.s.polMu.Lock()
	done := make(chan api.PolicyLockdownResultV1, 1)
	go func() {
		result, err := f.s.applyPolicyLockdownV1(api.PolicyLockdownArgsV1{Version: 1, On: false, Session: f.s.id, PreviousHash: previous})
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	f.s.mu.Lock()
	f.s.m.Layers[2].Rules = append(f.s.m.Layers[2].Rules, api.Rule{Allow: false, Target: "concurrent.example"})
	f.s.mu.Unlock()
	f.s.polMu.Unlock()
	select {
	case result := <-done:
		if result.State != "conflict" || result.Hash == previous {
			t.Fatal("concurrent mutation was replaced", result)
		}
	case <-time.After(time.Second):
		t.Fatal("lockdown CAS stalled")
	}
	select {
	case <-f.requests:
		t.Fatal("conflict reached the egress publisher")
	default:
	}
}

func TestLockdownWaitsForActualActorHashAndKeepsPendingNativeData(t *testing.T) {
	f, _ := replacementFixture(t)
	pending := json.RawMessage(`{"v":2,"future":{"keep":"bytes"}}`)
	f.s.m.PolicyNext = bytes.Clone(pending)
	previous, _ := api.HashPolicyLayerVectorV1(f.s.m.Layers)
	oldSave := saveManifest
	var saved *host.Manifest
	saveManifest = func(m *host.Manifest) error { copy := host.CloneManifest(*m); saved = &copy; return nil }
	t.Cleanup(func() { saveManifest = oldSave })
	type outcome struct {
		result api.PolicyLockdownResultV1
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.s.applyPolicyLockdownV1(api.PolicyLockdownArgsV1{Version: 1, On: true, Session: f.s.id, PreviousHash: previous})
		done <- outcome{result, err}
	}()
	capability := f.next(t)
	if capability.Op != egress.CtlCapabilities {
		t.Fatal("lockdown skipped actual actor negotiation")
	}
	raw, _ := json.Marshal(egress.CapabilitiesResult{Features: []string{api.FeaturePolicyLockdownV1}})
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: capability.ID, OK: true, Result: raw}); err != nil {
		t.Fatal(err)
	}
	request := f.next(t)
	var update egress.PolicyUpdateArgs
	if request.Op != egress.CtlPolicyUpdate || json.Unmarshal(request.Args, &update) != nil || update.Policy.Preset != api.PresetLocked || update.Policy.Unknown != api.UnknownDeny {
		t.Fatal("actual actor received no canonical restriction")
	}
	if saved != nil {
		t.Fatal("lockdown was journalled before actual egress acknowledgement")
	}
	hash, _ := api.HashPolicyLaptopLayersV1(update.SourceLayers)
	raw, _ = json.Marshal(map[string]string{"hash": hash})
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, OK: true, Result: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.State != "applied" {
			t.Fatal("actual lockdown failed", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("lockdown did not finish")
	}
	if saved == nil || !bytes.Equal(saved.PolicyNext, pending) || len(saved.Layers[2].Rules) != 1 || saved.Layers[2].Rules[0].Target != "session.example" {
		t.Fatal("lockdown interpreted pending data or changed underlying rules")
	}
}
