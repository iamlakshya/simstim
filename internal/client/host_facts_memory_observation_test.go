package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func factsMemorySampleV1(facts api.HostFacts) *api.MemoryObservationV1 {
	return &api.MemoryObservationV1{Version: 1, Complete: true, BootID: facts.BootID, At: facts.At,
		Reserved: facts.MemReserved, Charges: []api.MemoryChargeV1{{PrimaryID: "memory23", Charged: facts.MemReserved}}}
}

func factsWithMemoryRawV1(t *testing.T, facts api.HostFacts, sample string) []byte {
	t.Helper()
	facts.MemoryObservation = nil
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	return append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"memory_observation":`+sample+`}`)...)
}

func TestMemoryFactsCompleteSampleRetainedAndCloneOwned(t *testing.T) {
	facts := hostFactsFixtureValue()
	facts.MemoryObservation = factsMemorySampleV1(facts)
	facts.SlotCapacity = &api.SlotCapacityV1{Version: 1}
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeHostFactsContext(t.Context(), raw, facts.HostID)
	if err != nil || got.MemoryObservation == nil || got.SlotCapacity == nil || got.Room != facts.Room {
		t.Fatalf("complete same-scan metadata or unrelated facts lost: %v", err)
	}
	copy := cloneHostFacts(got)
	copy.MemoryObservation.Charges[0].Charged = 0
	copy.MemoryObservation.BootID = "another-boot"
	if got.MemoryObservation.Charges[0].Charged != facts.MemReserved || got.MemoryObservation.BootID != facts.BootID {
		t.Fatal("display or cache copy changed the original scan")
	}
}

func TestMemoryFactsUnusableOptionalSampleNeverAddsRoom(t *testing.T) {
	facts := hostFactsFixtureValue()
	valid, _ := json.Marshal(factsMemorySampleV1(facts))
	for _, sample := range []string{
		`null`, `true`, `[]`, `{}`, `{"v":2,"complete":"opaque","charges":false}`,
		strings.Replace(string(valid), `"complete":true`, `"complete":false`, 1),
		strings.Replace(string(valid), `"boot_id":"opaque-boot-id"`, `"boot_id":"other"`, 1),
		strings.Replace(string(valid), `"reserved":2147483648`, `"reserved":0`, 1),
		strings.Replace(string(valid), `"charges":[`, `"Charges":[`, 1),
		strings.TrimSuffix(string(valid), "}") + `,"v":1}`,
		strings.Replace(string(valid), `"charged":2147483648`, `"charged":-1`, 1),
		`{"v":1,"complete":true,"charges":[` + strings.Repeat(`{},`, api.MaxMemoryObservationChargesV1) + `{}` + `]}`,
		`{"v":1,"opaque":"` + strings.Repeat("x", api.MaxMemoryObservationBytesV1) + `"}`,
	} {
		got, err := decodeHostFactsContext(t.Context(), factsWithMemoryRawV1(t, facts, sample), facts.HostID)
		if err != nil || got.MemoryObservation != nil || got.Room != facts.Room || got.MemReserved != facts.MemReserved || !got.AccountingKnown {
			t.Fatalf("unusable optional sample changed enclosing facts, size=%d: %v", len(sample), err)
		}
	}
}

func TestMemoryFactsOptionalDataCannotHideKnownOrFutureSlotFailures(t *testing.T) {
	facts := hostFactsFixtureValue()
	bad := cloneHostFacts(facts)
	bad.Room++
	_, err := decodeHostFactsContext(t.Context(), factsWithMemoryRawV1(t, bad, `{"v":99}`), facts.HostID)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "facts_protocol" {
		t.Fatal("unusable optional metadata hid inconsistent native room", err)
	}
	raw := factsWithMemoryRawV1(t, facts, `{"v":99}`)
	raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"slot_capacity":{"v":2,"free":"opaque"}}`)...)
	_, err = decodeHostFactsContext(t.Context(), raw, facts.HostID)
	if !errors.As(err, &user) || user.Code != "facts_future" {
		t.Fatal("memory optional handling weakened future slot classification", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := decodeHostFactsContext(ctx, raw, facts.HostID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled facts decoding became an optional-data success", err)
	}
}
