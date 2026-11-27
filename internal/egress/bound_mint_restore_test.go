package egress

import (
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundColdMintedStandInsStayUnarmedAfterPrimaryArmAndNeedFreshMint(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	server, primary := newBoundRegistryServer(t, args)
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	oldSentinel, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := server.currentMembers().publicMinted(t.Context())
	if err != nil || len(saved["KEY"]) != 1 {
		t.Fatal("ready canonical mint did not expose exactly its safe persistence metadata")
	}
	args.Initial.Secrets = nil
	args.SourceKinds = nil
	args.Saved = primary.Secrets
	args.SavedMinted = api.ClonePublicMintedStates(saved)
	restored, initial := newBoundRegistryServer(t, args)
	mint := restored.currentMembers().secrets["KEY"].minted[0]
	if !mint.retired || mint.ready || mint.value != "" || mint.publication != nil || mint.sentinel != oldSentinel || len(initial.Minted["KEY"]) != 1 {
		t.Fatal("cold restore revived, discarded or refetched a server-issued credential")
	}
	assertUnarmed := func() {
		t.Helper()
		request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer " + string(oldSentinel)}})
		plan, failure := prepareRequestSecrets(restored.currentMembers().requestView, request, raw, checked, "https")
		if failure == nil || failure.status != http.StatusUnauthorized || len(plan.replacements) != 0 || !strings.Contains(failure.detail.Hint, "mint") {
			t.Fatal("old minted stand-in gained injection or an inaccurate attach-only recovery hint")
		}
		request, raw, checked = requestSecretFixture(t, "https", "example.com", "/v1?token="+string(oldSentinel), nil)
		if failure := checkRequestSecretTripwires(restored.currentMembers().requestView, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
			t.Fatal("permanently unarmed minted stand-in lost its URL tripwire")
		}
	}
	assertUnarmed()
	if _, err := restored.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}})); err != nil {
		t.Fatal(err)
	}
	assertUnarmed()
	fresh, err := restored.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(restored, "KEY"), value, "openai-api-key")
	if err != nil || fresh == oldSentinel || len(restored.currentMembers().secrets["KEY"].minted) != 2 {
		t.Fatal("fresh origin mint reused an old unarmed server-issued stand-in")
	}
	assertUnarmed()
}

func TestBoundMintedRestoreRefusesUnknownOwnerAuthorityRuleShapeAndPublicCollisions(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	server, initial := newBoundRegistryServer(t, args)
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	if _, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key"); err != nil {
		t.Fatal(err)
	}
	saved, err := server.currentMembers().publicMinted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string][]api.PublicMintedSecretState){
		func(m map[string][]api.PublicMintedSecretState) { m["UNKNOWN"] = m["KEY"]; delete(m, "KEY") },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"][0].Authority = "other.example.com:443" },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"][0].Identity = "not-server-owned" },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"][0].Rule = "unknown" },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"][0].Shape.Rule = "unknown" },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"][0].Shape.Prefix = "guessed" },
		func(m map[string][]api.PublicMintedSecretState) { m["KEY"] = append(m["KEY"], m["KEY"][0]) },
	} {
		candidate := args
		candidate.Initial.Secrets = nil
		candidate.SourceKinds = nil
		candidate.Saved = initial.Secrets
		candidate.SavedMinted = api.ClonePublicMintedStates(saved)
		mutate(candidate.SavedMinted)
		if _, err := prepareBoundInitial(t.Context(), candidate); err == nil {
			t.Fatal("invalid persisted minted public metadata gained a live namespace")
		}
	}
	candidate := args
	candidate.Initial.Secrets = nil
	candidate.SourceKinds = nil
	candidate.SavedMinted = api.ClonePublicMintedStates(saved)
	public := candidate.SavedMinted["KEY"][0]
	candidate.Saved = map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: public.Sentinel, Shape: public.Shape, Identity: public.Identity}}
	if _, err := prepareBoundInitial(t.Context(), candidate); err == nil {
		t.Fatal("retired minted identity borrowed a prior primary stand-in's header eligibility")
	}
}
