package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostFactsCannotPromotePartialOrForeignAccounting(t *testing.T) {
	now := time.Now().UTC()
	base := api.HostFacts{Version: api.HostFactsVersion, BootID: "boot", MemoryKnown: true, MemTotal: 8 << 30, MemForSessions: 6 << 30}
	exact := memoryRoomFactsV1{BootID: "boot", At: now, Known: true, Budget: 6 << 30, Reserved: 2 << 30, Room: 4 << 30, Sandboxes: 2}
	facts := base
	applyHostMemoryFactsV1(&facts, exact, nil)
	if !facts.AccountingKnown || facts.MemReserved != 2<<30 || facts.Room != 4<<30 || facts.Sandboxes != 2 {
		t.Fatalf("genuine observation lost: %+v", facts)
	}
	for _, change := range []func(*memoryRoomFactsV1){func(v *memoryRoomFactsV1) { v.Known = false }, func(v *memoryRoomFactsV1) { v.BootID = "old" }, func(v *memoryRoomFactsV1) { v.At = time.Time{} }, func(v *memoryRoomFactsV1) { v.Budget = 7 << 30 }, func(v *memoryRoomFactsV1) { v.Room = 5 << 30 }, func(v *memoryRoomFactsV1) { v.Reserved = -1 }, func(v *memoryRoomFactsV1) { v.Sandboxes = -1 }} {
		observed := exact
		change(&observed)
		facts = base
		applyHostMemoryFactsV1(&facts, observed, nil)
		if facts.AccountingKnown || facts.Room != 0 || facts.MemReserved != 0 || len(facts.Health) != 1 {
			t.Fatalf("unproved capacity promoted: %+v", facts)
		}
	}
	facts = base
	applyHostMemoryFactsV1(&facts, exact, errors.New("uncertain native unit"))
	if facts.AccountingKnown || facts.Room != 0 {
		t.Fatal("a native read error became known free room")
	}
}

func TestHostFactsRequestCannotSelectAnExclusionOrSupplyCapacity(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"exclude":"aaaa1111"}`, `{"room":999999999}`, `{"v":1}`, `{"original_operation":{}}`, `{} {}`} {
		var output bytes.Buffer
		b := &bridge{w: proto.NewWriter(&output)}
		if !b.handleMore(proto.Request{ID: 19, Op: "host.facts", Args: json.RawMessage(raw)}) {
			t.Fatal("facts operation was not dispatched")
		}
		frame, err := proto.Read(bufio.NewReader(&output))
		if err != nil {
			t.Fatal(err)
		}
		var response proto.Response
		if err = json.Unmarshal(frame.Payload, &response); err != nil {
			t.Fatal(err)
		}
		if response.ID != 19 || response.OK || response.Error == nil || response.Error.Code != "bad_request" || response.Error.Hint == "" {
			t.Fatalf("supplied accounting reached native observation: %+v", response)
		}
	}
}
