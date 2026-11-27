package egress

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundAdditiveMintsDoNotStaleAnOtherwiseApprovedPrimaryArm(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	for _, letter := range []string{"A", "C"} {
		before := server.currentMembers()
		value := api.Secret("sk-" + strings.Repeat(letter, 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
		if _, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key"); err != nil {
			t.Fatal(err)
		}
		if server.currentMembers() == before || server.currentMembers().generation != initial.Generation {
			t.Fatal("additive mint either failed to replace its immutable view or rotated primary authority CAS")
		}
	}
	armed, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil || armed.Generation == initial.Generation {
		t.Fatal("ordinary minted responses made an approved primary arm stale or weakened its CAS")
	}
}

func TestBoundReceiptRetryDoesNotRotateAuthorityOrReuseAStaleAcknowledgement(t *testing.T) {
	f, cancel, first, old, value := startUnacknowledgedMint(t)
	generation := f.server.currentMembers().generation
	cancel()
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled first receipt did not release its response")
	}
	second := make(chan boundMintResult, 1)
	go func() {
		sentinel, err := f.server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(f.server, "KEY"), value, "openai-api-key")
		second <- boundMintResult{sentinel, err}
	}()
	current := f.nextMint(t)
	if current.Receipt == old.Receipt || current.Generation != generation || current.State.Identity != old.State.Identity || current.State.Sentinel != old.State.Sentinel || len(f.server.currentMembers().secrets["KEY"].minted) != 1 {
		t.Fatal("receipt retry changed authority/variant identity or duplicated a canonical value")
	}
	if err := f.server.acknowledgeMintedState(memberJSON(t, SecretSavedArgs{Version: 2, Generation: old.Generation, Receipt: old.Receipt, Owner: old.Owner, Identity: old.State.Identity, Durable: true})); err == nil {
		t.Fatal("stale receipt acknowledged a different live reservation")
	}
	if err := f.acknowledge(current, true); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-second:
		if result.err != nil || result.sentinel != old.State.Sentinel || f.server.currentMembers().generation != generation {
			t.Fatal("exact durable retry did not preserve authority and the same public variant")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable retry did not release its owned response")
	}
}

func TestBoundMemberValueChangeInvalidatesThePreviousPendingMintCohort(t *testing.T) {
	f, _, result, event, _ := startUnacknowledgedMint(t)
	before := f.server.currentMembers().generation
	if _, err := f.server.addBoundControlMember(memberJSON(t, memberIntent(before, map[string]api.Secret{"KEY": "WXYZ1111"}, "codex"))); err != nil {
		t.Fatal(err)
	}
	if f.server.currentMembers().generation == before {
		t.Fatal("primary/member authority change kept the previous CAS")
	}
	if err := f.server.acknowledgeMintedState(memberJSON(t, SecretSavedArgs{Version: 2, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: true})); err == nil {
		t.Fatal("new member rotation accepted an old private cohort's mint receipt")
	}
	select {
	case completed := <-result:
		if completed.err == nil || completed.sentinel != "" {
			t.Fatal("old member cohort emitted a stand-in after its value changed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("invalidated member cohort retained its response")
	}
}
