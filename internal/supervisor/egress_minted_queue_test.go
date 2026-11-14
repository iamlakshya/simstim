package supervisor

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func queuedMintFixture(receipt, generation string) egress.MintedStateEvent {
	return egress.MintedStateEvent{Version: egress.MemberRegistryV2, Generation: generation, Receipt: receipt, Owner: "CREDENTIAL", State: api.PublicMintedSecretState{
		Identity: "11111111111111111111111111111111", Authority: "example.com:443", Rule: "fixture-rule", Sentinel: api.Secret("stand-in"),
		Shape: &api.SecretShape{Version: api.SecretShapeV1, Rule: "fixture-rule", Slots: []api.SecretShapeSlot{{Class: "lower", Count: 1, Width: 1}}},
	}}
}

func TestMintedQueueCoalescesOnlyExactStateAndReceipt(t *testing.T) {
	q := newMintedQueue()
	event := queuedMintFixture("receipt-one", "generation-one")
	for range 1000 {
		if !q.enqueue(event, 400) {
			t.Fatal("identical queued receipt refused")
		}
	}
	if q.bytes != 400 || len(q.order) != 1 || len(q.groups) != 1 {
		t.Fatal("duplicate receipt grew the queue")
	}
	retry := event
	retry.Receipt, retry.Generation = "receipt-two", "generation-two"
	if !q.enqueue(retry, 400) || len(q.order) != 2 || q.bytes != 800 || len(q.groups) != 1 {
		t.Fatal("distinct correlated retry was dropped or copied a second state")
	}
	conflict := event
	conflict.State.Sentinel = api.Secret("other-stand-in")
	if q.enqueue(conflict, 400) || len(q.order) != 2 {
		t.Fatal("same identity admitted contradictory public metadata")
	}
	first, ok := q.front()
	if !ok || first.Receipt != event.Receipt || first.Generation != event.Generation {
		t.Fatal("new global generation replaced an earlier receipt")
	}
	q.complete()
	second, ok := q.front()
	if !ok || second.Receipt != retry.Receipt || second.Generation != retry.Generation {
		t.Fatal("retry lost its exact receipt labels")
	}
	q.complete()
	if len(q.groups) != 0 || q.bytes != 0 || len(q.order) != 0 {
		t.Fatal("completed receipts retained history")
	}
}

func TestMintedQueueChargesActiveWorkAndRejectsPressureLocally(t *testing.T) {
	q := newMintedQueue()
	first := queuedMintFixture("receipt-one", "generation-one")
	if !q.enqueue(first, proto.MaxFrame) {
		t.Fatal("existing complete frame budget was narrowed")
	}
	if _, ok := q.front(); !ok || q.bytes != proto.MaxFrame {
		t.Fatal("active disk/RPC work escaped the budget")
	}
	second := first
	second.Receipt = "receipt-two"
	if q.enqueue(second, 1) || q.bytes != proto.MaxFrame || len(q.order) != 1 {
		t.Fatal("pressure grew the queue or consumed an admitted receipt")
	}
	q.complete()
	if !q.enqueue(second, 1) {
		t.Fatal("pressure poisoned later admissions")
	}
	q.close()
	if q.enqueue(first, 1) || q.bytes != 0 || q.groups != nil || q.order != nil {
		t.Fatal("closed queue retained work or admitted more receipts")
	}
}

func TestMintedQueueOwnsShapeAndPreservesReceiptFIFO(t *testing.T) {
	q := newMintedQueue()
	first := queuedMintFixture("receipt-one", "generation-one")
	if !q.enqueue(first, 400) {
		t.Fatal("first receipt refused")
	}
	first.State.Shape.Slots[0].Count = 100
	got, _ := q.front()
	if got.State.Shape.Slots[0].Count != 1 {
		t.Fatal("queue borrowed mutable public shape storage")
	}
	other := queuedMintFixture("receipt-other", "generation-two")
	other.State.Identity = "22222222222222222222222222222222"
	if !q.enqueue(other, 400) {
		t.Fatal("second public identity refused")
	}
	retry := got
	retry.Receipt = "receipt-retry"
	if !q.enqueue(retry, 400) {
		t.Fatal("retry refused")
	}
	q.complete()
	got, _ = q.front()
	if got.Receipt != other.Receipt {
		t.Fatal("coalescing allowed new retries to starve an earlier identity")
	}
}

func TestMintedQueueHonoursActualEncodedFrameBudget(t *testing.T) {
	q := newMintedQueue()
	event := queuedMintFixture("receipt-one", "generation-one")
	payload, err := json.Marshal(egress.CtlEvent{Event: egress.EventSecretMinted, Minted: &event})
	if err != nil {
		t.Fatal(err)
	}
	if !q.enqueue(event, len(payload)+1) || q.bytes != len(payload)+1 {
		t.Fatal("type byte or encoded public proposal was omitted from charging")
	}
	for _, bytes := range []int{-1, 0, proto.MaxFrame + 1} {
		if newMintedQueue().enqueue(event, bytes) {
			t.Fatal("invalid frame charge accepted")
		}
	}
}
