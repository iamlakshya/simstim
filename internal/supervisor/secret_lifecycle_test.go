package supervisor

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestChildInfoUsesCurrentSharedCASAfterSiblingMutation(t *testing.T) {
	state, _ := validPublicMintFixture()
	root := &Supervisor{id: "root2222", m: &host.Manifest{ID: "root2222", SecretState: state}}
	stale := api.CloneSessionSecretState(state)
	child := &Supervisor{id: "child222", primary: root, m: &host.Manifest{ID: "child222", Name: "selected", SandboxOf: root.id, SecretState: stale}}
	// A sibling membership or successful Arm committed this new root CAS.
	root.m.SecretState.Generation = strings.Repeat("f", 32)
	info, err := child.snapshotForInfo()
	if err != nil || info.ID != child.id || info.Name != "selected" || info.SandboxOf != root.id || info.SecretState.Generation != root.m.SecretState.Generation || info.SecretState.Generation == stale.Generation {
		t.Fatal("child refresh kept a stale CAS or rewrote the selected identity")
	}
	public := info.SecretState.Primary["CREDENTIAL"]
	public.Shape.Slots[0].Count = 99
	if root.m.SecretState.Primary["CREDENTIAL"].Shape.Slots[0].Count == 99 {
		t.Fatal("public info aliases mutable root metadata")
	}
}

func TestPublicDisarmRetainsStandInsAndFutureOpaqueState(t *testing.T) {
	state, _ := validPublicMintFixture()
	original := state.Primary["CREDENTIAL"]
	manifest := &host.Manifest{SecretState: state}
	if !disarmPublicSecretsLocked(manifest) || manifest.SecretState.Primary["CREDENTIAL"].Armed || manifest.SecretState.Primary["CREDENTIAL"].Sentinel != original.Sentinel || manifest.SecretState.Primary["CREDENTIAL"].Identity != original.Identity {
		t.Fatal("egress memory loss retained availability or lost public identity")
	}
	if disarmPublicSecretsLocked(manifest) {
		t.Fatal("repeated disarm changed an already cold public view")
	}
	raw := []byte(`{"version":99,"future":"opaque-public-marker"}`)
	var unknown api.SessionSecretState
	if err := json.Unmarshal(raw, &unknown); err != nil {
		t.Fatal(err)
	}
	manifest.SecretState = &unknown
	if disarmPublicSecretsLocked(manifest) {
		t.Fatal("opaque future state was interpreted")
	}
	encoded, err := json.Marshal(manifest.SecretState)
	if err != nil || string(encoded) != string(raw) {
		t.Fatal("lifecycle disarm rewrote unknown saved bytes")
	}
}

func TestLegacyRunningRegistryDoesNotAdvertiseFreshBoundPublication(t *testing.T) {
	root := &Supervisor{memberReady: true, m: &host.Manifest{}}
	if slices.Contains(root.sessionFeatures(), api.FeatureBoundSecrets) {
		t.Fatal("legacy running registry advertised an unusable bound producer")
	}
	state, _ := validPublicMintFixture()
	root.m.SecretState = state
	if !slices.Contains(root.sessionFeatures(), api.FeatureBoundSecrets) {
		t.Fatal("actual new-form registry lost its declared capability")
	}
}
