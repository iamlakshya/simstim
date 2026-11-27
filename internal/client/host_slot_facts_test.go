package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSlotFactsStrictProjectionCloneAndUnknown(t *testing.T) {
	facts := hostFactsFixtureValue()
	facts.SlotCapacity = &api.SlotCapacityV1{Version: 1, Known: true, Total: 16, Occupied: 3, Free: 11, Quarantined: 2, At: facts.At, Generation: strings.Repeat("a", 32)}
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeHostFacts(raw, facts.HostID)
	if err != nil || got.SlotCapacity == nil || got.SlotCapacity.Free != 11 {
		t.Fatalf("valid advisory count refused: %v", err)
	}
	clone := cloneHostFacts(got)
	clone.SlotCapacity.Free = 0
	if got.SlotCapacity.Free != 11 {
		t.Fatal("the facts clone borrowed a mutable capacity pointer")
	}
	for _, slot := range []*api.SlotCapacityV1{nil, {Version: 1}} {
		facts.SlotCapacity = slot
		raw, _ := json.Marshal(facts)
		if _, err := decodeHostFacts(raw, facts.HostID); err != nil {
			t.Fatal("unknown advisory capacity refused", err)
		}
	}
	for _, slot := range []string{
		`{"v":1,"known":false,"free":16}`,
		`{"v":1,"known":true,"total":16,"free":17}`,
		`{"v":1,"known":false,"Free":0}`,
		`{"v":1,"known":false,"known":false}`,
	} {
		facts.SlotCapacity = nil
		raw, _ := json.Marshal(facts)
		raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"slot_capacity":`+slot+`}`)...)
		if _, err := decodeHostFacts(raw, facts.HostID); err == nil {
			t.Fatalf("malformed capacity became selectable data: %s", slot)
		}
	}
}

func TestSlotFactsFutureBodyStaysOpaqueAfterOtherKnownFacts(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		facts := hostFactsFixtureValue()
		if invalid {
			facts.Room++
		}
		raw, _ := json.Marshal(facts)
		raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"slot_capacity":{"v":2,"known":"opaque","free":{"anything":true}}}`)...)
		_, err := decodeHostFacts(raw, facts.HostID)
		var user *UserError
		want := "facts_future"
		if invalid {
			want = "facts_protocol"
		}
		if !errors.As(err, &user) || user.Code != want {
			t.Fatalf("future slot body hid known facts: want %s got %v", want, err)
		}
	}
}
