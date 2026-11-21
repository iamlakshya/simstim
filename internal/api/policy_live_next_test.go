package api

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestPendingPolicyCapsuleNeverRestoresStaleSessionRules(t *testing.T) {
	applied := liveSourcesFixtureV1()
	selected, err := PolicyLaptopLayersV1(applied)
	if err != nil {
		t.Fatal(err)
	}
	selected[0].Sandbox.Secrets = []string{"NEXT"}
	raw, err := EncodePolicyNextLayersV1(selected)
	if err != nil {
		t.Fatal(err)
	}
	applied[6].Rules = []Rule{{Door: DoorNet, Target: "current.example", GrantID: "current-instance"}}
	desired, err := DesiredPolicyLayersV1(applied, raw)
	if err != nil || len(desired[6].Rules) != 1 || desired[6].Rules[0].GrantID != "current-instance" || desired[1].Sandbox.Secrets[0] != "NEXT" {
		t.Fatal("pending laptop metadata replaced current excluded provenance", err)
	}
	if PolicyNextMatchesAppliedV1(applied, raw) {
		t.Fatal("pending nonlive source became an applied readiness claim")
	}
	if !PolicyNextMatchesAppliedV1(desired, raw) {
		t.Fatal("exact selected applied projection did not match its capsule")
	}
	desired[1].Sandbox.Secrets[0] = "MUTATED"
	if selected[0].Sandbox.Secrets[0] != "NEXT" {
		t.Fatal("capsule projection borrowed mutable source data")
	}
}

func TestPendingPolicyFutureOrMalformedCapsuleStaysOpaqueAndCannotMatch(t *testing.T) {
	for _, raw := range []json.RawMessage{
		[]byte(`{"v":2,"future":{"keep":"bytes"},"layers":[]}`),
		[]byte(`{"v":1,"V":1,"layers":[]}`),
		[]byte(`{"v":1,"v":1,"layers":[]}`),
		[]byte(`{"v":1,"layers":[{"kind":"session","rules":[{"target":"stale.example","allow":true}]}]}`),
	} {
		original := slices.Clone(raw)
		if _, err := DesiredPolicyLayersV1(liveSourcesFixtureV1(), raw); err == nil || PolicyNextMatchesAppliedV1(liveSourcesFixtureV1(), raw) || !bytes.Equal(original, raw) {
			t.Fatal("unsupported pending data became an applied source or was rewritten")
		}
	}
}

func TestPendingPolicyMatchingUsesExactJSONProjectionWithoutHashOrNilCollectionTraps(t *testing.T) {
	applied := liveSourcesFixtureV1()
	applied[1].Rules = []Rule{}
	selected, err := PolicyLaptopLayersV1(applied)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodePolicyNextLayersV1(selected)
	if err != nil || !PolicyNextMatchesAppliedV1(applied, raw) {
		t.Fatal("a serialised omitempty collection prevented an exact projection match", err)
	}
	applied[1].Network.Rules = []NetworkRule{{Target: "exact.example", Allow: true, Methods: new([]string{})}}
	if PolicyNextMatchesAppliedV1(applied, raw) {
		t.Fatal("changed selected methods became an exact applied projection")
	}
}
