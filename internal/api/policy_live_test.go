package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func liveSourcesFixtureV1() []Layer {
	return []Layer{{Kind: LayerBuiltin}, {Kind: LayerAll}, {Kind: LayerHost, Name: "box"}, {Kind: LayerRepo, Name: "forge.example/team/app"}, {Kind: LayerRepo, Name: "forge.example/team/app", Source: SourceRepositoryFile}, {Kind: LayerProfile, Name: "review"}, {Kind: LayerSession}, {Kind: LayerLockdown}}
}

func TestPolicySourceHashUsesExactLaptopVectorAndExcludesSessionChanges(t *testing.T) {
	sources := liveSourcesFixtureV1()
	sources[1].Network.Deny = []string{"first.example"}
	laptop, err := PolicyLaptopLayersV1(sources)
	if err != nil || len(laptop) != 4 {
		t.Fatal("wrong laptop source vector", err)
	}
	encoded, err := json.Marshal(laptop)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	hash, err := HashPolicyLaptopLayersV1(sources)
	if err != nil || hash != hex.EncodeToString(sum[:]) {
		t.Fatal("source hash does not bind the actual carried JSON", hash, err)
	}
	sources[6].Rules = []Rule{{Allow: true, Target: "session.example"}}
	sources[4].Network.Deny = []string{"repository-file.example"}
	sources[7].Preset = PresetLocked
	if after, err := HashPolicyLaptopLayersV1(sources); err != nil || after != hash {
		t.Fatal("excluded policy changed the laptop layer hash", after, err)
	}
	laptop[0].Network.Deny[0] = "changed.example"
	if sources[1].Network.Deny[0] != "first.example" {
		t.Fatal("hash source selection returned borrowed source memory")
	}
}

func TestPolicyReplacementPreservesProtectedSourcesAndOriginalIdentityOrder(t *testing.T) {
	sources := liveSourcesFixtureV1()
	updates, err := PolicyLaptopLayersV1(sources)
	if err != nil {
		t.Fatal(err)
	}
	updates[0].Network.Allow = []string{"new.example"}
	next, err := ReplacePolicyLaptopLayersV1(sources, updates)
	if err != nil || !reflect.DeepEqual(next[4], sources[4]) || !reflect.DeepEqual(next[6:], sources[6:]) || next[1].Network.Allow[0] != "new.example" {
		t.Fatal("replacement altered protected sources or failed to replace the selected layer", err)
	}
	next[1].Network.Allow[0] = "mutated.example"
	if updates[0].Network.Allow[0] != "new.example" {
		t.Fatal("replacement borrowed its caller's layer data")
	}
	for _, change := range []func([]Layer){
		func(l []Layer) { l[1].Name = "other" },
		func(l []Layer) { l[2], l[3] = l[3], l[2] },
		func(l []Layer) { l[2].Source = SourceRepositoryFile },
		func(l []Layer) { l[2] = l[1] },
	} {
		bad := CloneLayers(updates)
		change(bad)
		if _, err := ReplacePolicyLaptopLayersV1(sources, bad); err == nil {
			t.Fatal("replacement added, aliased or reordered selected source identities")
		}
	}
}

func TestLivePolicyResultClosesVersionDispositionAndNextStartFields(t *testing.T) {
	hash, err := HashPolicyLaptopLayersV1(liveSourcesFixtureV1())
	if err != nil {
		t.Fatal(err)
	}
	valid := PolicyReplaceResultV1{Version: 1, Hash: hash, State: "applied", NextStart: []string{"sandbox.mounts", "sandbox.ssh_keys"}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PolicyReplaceResultV1){
		func(r *PolicyReplaceResultV1) { r.Version = 2 },
		func(r *PolicyReplaceResultV1) { r.State = "maybe" },
		func(r *PolicyReplaceResultV1) { r.Hash = "HASH" },
		func(r *PolicyReplaceResultV1) { r.State = "conflict" },
		func(r *PolicyReplaceResultV1) { r.NextStart = []string{"network.model_budget"} },
		func(r *PolicyReplaceResultV1) { r.NextStart = []string{"sandbox.paste", "sandbox.paste"} },
	} {
		bad := valid
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("unsupported response became a live acknowledgement")
		}
	}
}
