package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func lockdownPolicy(t *testing.T, on bool) api.Policy {
	t.Helper()
	sources := []api.Layer{{Kind: api.LayerAll, Rules: []api.Rule{{Door: api.DoorNet, Target: "broader.example", Allow: true}, {Door: api.DoorNet, Target: "denied.example", Allow: true}}}, {Kind: api.LayerSession, Rules: []api.Rule{{Door: api.DoorNet, Target: "session.example", Allow: true}, {Door: api.DoorNet, Target: "once.example", Allow: true, Once: true, GrantID: "lockdown-kept-once"}, {Door: api.DoorNet, Target: "denied.example"}}}}
	if on {
		sources = append(sources, api.Layer{Kind: api.LayerLockdown, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: new("locked")}, Network: api.NetworkValues{Unknown: new("deny")}}})
	}
	policy, _, err := MergeLayers(sources)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestLockdownPausesSessionAllowancesAndPreservesDeniesBroaderRulesAndOnceLedger(t *testing.T) {
	server := newMemberServer(t, InitArgs{SessionID: "lockdown2", Agent: "shell", Policy: lockdownPolicy(t, false)})
	once := Request{Host: "once.example", Port: 443}
	permit := server.currentEngine().NewPermit()
	if !server.currentEngine().DecideWithPermit(once, permit).Allow {
		t.Fatal("once fixture refused")
	}
	permit.Commit()
	permit.Close()
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: lockdownPolicy(t, true)})); err != nil {
		t.Fatal(err)
	}
	engine := server.currentEngine()
	if !engine.lockdown || engine.preset != api.PresetLocked || engine.unknown != api.UnknownDeny || engine.Decide(Request{Host: "session.example", Port: 443}).Allow || !engine.Decide(Request{Host: "broader.example", Port: 443}).Allow || engine.Decide(Request{Host: "denied.example", Port: 443}).Allow {
		t.Fatal("lockdown lost a broader allow or session deny, or retained a session allowance")
	}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: lockdownPolicy(t, false)})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine().Decide(once).Allow || !server.currentEngine().Decide(Request{Host: "session.example", Port: 443}).Allow {
		t.Fatal("unlock revived spent once or failed to resume paused session allowance")
	}
}

func TestLockdownPausesActualPrivateSessionAndOnceRows(t *testing.T) {
	server := newMemberServer(t, InitArgs{SessionID: "answer2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"KEY": "authored.example"}})
	args := heldAnswerArgs(t, server, "authored.example", api.NetworkHoldSession)
	if _, err := server.answerNetworkHold(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	args = heldAnswerArgs(t, server, "other.example", api.NetworkHoldOnce)
	if _, err := server.answerNetworkHold(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: lockdownPolicy(t, true)})); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"authored.example", "other.example"} {
		if server.currentEngine().Decide(Request{Host: host, Port: 443}).Allow {
			t.Fatal("lockdown retained actual private session/once authority")
		}
	}
}

func TestEmptyNativeLockLayerProjectsItsEffectiveSettingsWithoutChangingLaptopHash(t *testing.T) {
	sources := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: new("balanced")}, Network: api.NetworkValues{Unknown: new("ask")}}, Lock: &api.Lock{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: new("balanced")}}}}, {Kind: api.LayerSession}}
	before, canonical, err := MergeLayers(sources)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := api.HashPolicyLaptopLayersV1(canonical)
	if err != nil {
		t.Fatal(err)
	}
	locked, withLock, err := MergeLayers(append(api.CloneLayers(canonical), api.Layer{Kind: api.LayerLockdown}))
	if err != nil {
		t.Fatal("emergency lock depended on ordinary preset-lock permission", err)
	}
	afterHash, err := api.HashPolicyLaptopLayersV1(withLock)
	if err != nil || afterHash != hash {
		t.Fatal("native lock changed laptop identityhash", err)
	}
	if locked.Preset != api.PresetLocked || locked.Unknown != api.UnknownDeny || *locked.Sandbox.Preset != "locked" || *locked.Network.Unknown != "deny" || before.Preset != api.PresetBalanced {
		t.Fatal("actual public policy projection disagreed with emergency enforcement")
	}
	found := 0
	for _, origin := range locked.Origins {
		if origin.Key == "sandbox.preset" || origin.Key == "network.unknown" {
			if origin.Layer != api.LayerLockdown {
				t.Fatal("wrong override origin")
			}
			found++
		}
	}
	if found != 2 {
		t.Fatal("lock effective values have no exact source origins")
	}
}
