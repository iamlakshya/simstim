package egress

import (
	"reflect"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLivePolicyProjectionAppliesNetworkAndPushWhilePreservingEveryOtherSandboxField(t *testing.T) {
	before := api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: new("process"), Image: new("old"), Secrets: []string{"OLD"}, SSHKeys: []string{"first"}, Mounts: []string{"/old"}, Paste: new("media"), Decoys: new(true)}}}
	desired := api.ClonePolicy(before)
	desired.Sandbox.Wall, desired.Sandbox.Image = new("microvm"), new("new")
	desired.Sandbox.Secrets, desired.Sandbox.SSHKeys, desired.Sandbox.Mounts = []string{"NEW"}, []string{"second"}, []string{"/new"}
	desired.Sandbox.Paste, desired.Sandbox.Decoys = new("any"), new(false)
	desired.Sandbox.Preset, desired.Sandbox.Push = new("locked"), new(true)
	desired.Network.ModelBudget = new(int64(10))
	desired.Classifier.Judge = new("none")
	desired.Rules = []api.Rule{{Door: api.DoorNet, Target: "new.example", Allow: true}, {Door: api.DoorPush, Target: "forge.example/team/app", Allow: true}}
	live, next := projectLivePolicyV1(before, desired)
	wantSandbox := api.CloneLayerValues(before.LayerValues).Sandbox
	wantSandbox.Preset, wantSandbox.Push = desired.Sandbox.Preset, desired.Sandbox.Push
	if !reflect.DeepEqual(live.Sandbox, wantSandbox) || !reflect.DeepEqual(live.Network, desired.Network) || !reflect.DeepEqual(live.Classifier, desired.Classifier) || !reflect.DeepEqual(live.Rules, desired.Rules) {
		t.Fatal("next-start native/secret values became live, or live network fields stayed old")
	}
	for _, key := range []string{"sandbox.decoys", "sandbox.image", "sandbox.mounts", "sandbox.paste", "sandbox.secrets", "sandbox.ssh_keys", "sandbox.wall"} {
		if !slices.Contains(next, key) {
			t.Fatalf("changed next-start field was not reported: %s in %q", key, next)
		}
	}
	if err := (api.PolicyReplaceResultV1{Version: 1, Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "applied", NextStart: next}).Validate(); err != nil {
		t.Fatal("projection made unsupported next-start keys", err)
	}
	live.Sandbox.Secrets[0] = "MUTATED"
	*live.Network.ModelBudget = 0
	if before.Sandbox.Secrets[0] != "OLD" || *desired.Network.ModelBudget != 10 {
		t.Fatal("live projection borrowed original applied state or its caller's proposal")
	}
}

func TestLivePolicyProjectionKeepsNonliveRulesAndRemovedNonliveLocks(t *testing.T) {
	before := api.Policy{Rules: []api.Rule{{Door: api.DoorPort, Target: "3000", Allow: true}}, Locks: []api.Lock{{Layer: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: new("media")}}, Deny: []string{"old.example"}}}}
	desired := api.Policy{Rules: []api.Rule{{Door: api.DoorPort, Target: "4000", Allow: true}, {Door: api.DoorNet, Target: "new.example", Allow: true}}}
	live, next := projectLivePolicyV1(before, desired)
	if len(live.Rules) != 2 || live.Rules[0].Target != "3000" || live.Rules[1].Target != "new.example" || len(live.Locks) != 1 || live.Locks[0].Sandbox.Paste == nil || *live.Locks[0].Sandbox.Paste != "media" || len(live.Locks[0].Deny) != 0 || !slices.Contains(next, "rules") || !slices.Contains(next, "locks") {
		t.Fatal("nonlive lock/door changed early or a removed live deny survived", live, next)
	}
}

func TestLivePolicyPreparationHashesTheCanonicalCarriedPacketAndEnforcesCurrentLocks(t *testing.T) {
	currentSources := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: new("process")}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}}
	applied, canonical, err := MergeLayers(api.CloneLayers(currentSources))
	if err != nil {
		t.Fatal(err)
	}
	proposal := api.CloneLayers(canonical)
	proposal[0].Sandbox.Wall = new("microvm")
	proposal[0].Network.Deny = []string{"blocked.example"}
	_, proposal, err = MergeLayers(proposal)
	if err != nil {
		t.Fatal(err)
	}
	live, hash, next, err := PrepareLivePolicyV1(applied, proposal)
	wantHash, hashErr := api.HashPolicyLaptopLayersV1(proposal)
	if err != nil || hashErr != nil || hash != wantHash || *live.Sandbox.Wall != "process" || !slices.Contains(next, "sandbox.wall") || len(live.Rules) != 1 || live.Rules[0].Target != "blocked.example" || live.Rules[0].Allow {
		t.Fatal("preparation echoed a caller hash, lost the live deny or applied the wall early", hash, next, err, hashErr)
	}
	locked := api.CloneLayers(proposal)
	locked[0].Lock = &api.Lock{Deny: []string{"blocked.example"}}
	locked[2].Network.Allow = []string{"blocked.example"}
	if _, _, _, err := PrepareLivePolicyV1(applied, locked); err == nil {
		t.Fatal("replacement bypassed the existing layer lock merger")
	}
}
