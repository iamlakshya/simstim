package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type boundMintResult struct {
	sentinel api.Secret
	err      error
}

func startUnacknowledgedMint(t *testing.T) (*boundControlFixture, context.CancelFunc, <-chan boundMintResult, MintedStateEvent, api.Secret) {
	t.Helper()
	f := newBoundControlFixture(t, false)
	if _, err := f.server.initialiseBoundControl(memberJSON(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))); err != nil {
		t.Fatal(err)
	}
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan boundMintResult, 1)
	go func() {
		sentinel, err := f.server.registerResponseSecret(ctx, "example.com:443", boundPrimaryUse(f.server, "KEY"), value, "openai-api-key")
		result <- boundMintResult{sentinel, err}
	}()
	return f, cancel, result, f.nextMint(t), value
}

func TestBoundMintWaitsForDurableExactReceiptAndBlocksEveryPendingEcho(t *testing.T) {
	f, _, result, event, value := startUnacknowledgedMint(t)
	if event.Owner != "KEY" || !validRegistryName(event.Owner) || event.State.Identity == f.server.currentMembers().secrets["KEY"].identity {
		t.Fatal("mint used a rule ID as owner or reused its primary request identity")
	}
	public, err := json.Marshal(event)
	if err != nil || bytes.Contains(public, []byte(value)) || bytes.Contains(public, []byte("history")) {
		t.Fatal("durable metadata event contained actual values or history")
	}
	select {
	case <-result:
		t.Fatal("new public stand-in escaped before durable acknowledgement")
	default:
	}
	view, err := f.server.responseSecretSnapshot(t.Context(), "example.com:443", boundPrimaryUse(f.server, "KEY"))
	if err != nil || view.match([]byte(string(value)+":"), true).err == nil {
		t.Fatal("pending real value beside a non-minted delimiter was not an unresolved echo candidate")
	}
	request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer " + string(event.State.Sentinel)}})
	if plan, failure := prepareRequestSecrets(f.server.currentMembers().requestView, request, raw, checked, "https"); failure == nil || failure.status != http.StatusUnauthorized || len(plan.replacements) != 0 {
		t.Fatal("undurable public stand-in already injected its actual minted credential")
	}
	if err := f.acknowledge(event, true); err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-result:
		if completed.err != nil || completed.sentinel != event.State.Sentinel {
			t.Fatal("durable exact receipt did not complete the same canonical reservation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("durable ACK could not enter the registry while its response was waiting")
	}
	if !f.server.currentMembers().secrets["KEY"].minted[0].ready {
		t.Fatal("successful durable ACK returned before ready-view publication")
	}
	view, err = f.server.responseSecretSnapshot(t.Context(), "example.com:443", secretRequestUse{Names: []string{"KEY"}, Identities: []string{event.State.Identity}})
	if actual, valid := view.exact(value); err != nil || !valid || actual != event.State.Sentinel {
		t.Fatal("validated minted request variant did not select its parent binding echo identity")
	}
}

func TestBoundNegativeOrCancelledMintReceiptAffectsOnlyItsOwnedResponse(t *testing.T) {
	for _, negative := range []bool{false, true} {
		f, cancel, result, event, _ := startUnacknowledgedMint(t)
		if negative {
			if err := f.acknowledge(event, false); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		select {
		case completed := <-result:
			if completed.err == nil || completed.sentinel != "" {
				t.Fatal("failed durable reservation emitted its public stand-in")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("failed durable receipt retained its owned response")
		}
		select {
		case <-f.server.Done():
			t.Fatal("a failed receipt closed unrelated egress ownership")
		case <-f.server.sess.Done():
			t.Fatal("a failed receipt closed the healthy shared control peer")
		default:
		}
		if f.server.currentMembers().secrets["KEY"].minted[0].ready {
			t.Fatal("failed receipt made an undurable minted record request-eligible")
		}
		if !negative {
			if err := f.server.acknowledgeMintedState(memberJSON(t, SecretSavedArgs{Version: 2, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: true})); err == nil {
				t.Fatal("late ACK revived a receipt whose only request was cancelled")
			}
		}
	}
}

func TestBoundMintReceiptRejectsStaleLabelsAndChangedParentEpoch(t *testing.T) {
	f, cancel, result, event, _ := startUnacknowledgedMint(t)
	for _, mutate := range []func(*SecretSavedArgs){
		func(a *SecretSavedArgs) { a.Receipt = "other" },
		func(a *SecretSavedArgs) { a.Generation = "other" },
		func(a *SecretSavedArgs) { a.Owner = "OTHER" },
		func(a *SecretSavedArgs) { a.Identity = "other" },
		func(a *SecretSavedArgs) { a.Version = 1 },
	} {
		args := SecretSavedArgs{Version: 2, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: true}
		mutate(&args)
		if err := f.server.acknowledgeMintedState(memberJSON(t, args)); err == nil || f.server.currentMembers().secrets["KEY"].minted[0].ready {
			t.Fatal("stale receipt labels changed active minted readiness")
		}
	}
	current := f.server.currentMembers()
	if _, err := f.server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: current.generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}})); err != nil {
		t.Fatal(err)
	}
	if err := f.server.acknowledgeMintedState(memberJSON(t, SecretSavedArgs{Version: 2, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: true})); err == nil {
		t.Fatal("receipt from the previous parent value epoch was accepted")
	}
	cancel()
	select {
	case completed := <-result:
		if completed.err == nil || completed.sentinel != "" {
			t.Fatal("old parent epoch emitted a fresh response stand-in")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retired parent epoch did not release its response waiter")
	}
}

func TestBoundConcurrentMintWaitersShareReceiptAndOneCancellationDoesNotRetireTheOther(t *testing.T) {
	f, cancelFirst, first, event, value := startUnacknowledgedMint(t)
	second := make(chan boundMintResult, 1)
	go func() {
		sentinel, err := f.server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(f.server, "KEY"), value, "openai-api-key")
		second <- boundMintResult{sentinel, err}
	}()
	p := f.server.currentMembers().secrets["KEY"].minted[0].publication
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		p.state.mu.Lock()
		count := len(p.state.waiters)
		p.state.mu.Unlock()
		if count == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("concurrent callbacks did not share the actual canonical receipt")
		}
	}
	cancelFirst()
	select {
	case result := <-first:
		if result.err == nil || result.sentinel != "" {
			t.Fatal("cancelled owner emitted a public stand-in")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled receipt waiter did not leave independently")
	}
	if err := f.acknowledge(event, true); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-second:
		if result.err != nil || result.sentinel != event.State.Sentinel {
			t.Fatal("one cancelled waiter retired the other request's durable reservation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remaining durable waiter did not complete")
	}
	select {
	case <-f.events:
		t.Fatal("concurrent idempotent callbacks emitted duplicate metadata receipts")
	default:
	}
}
