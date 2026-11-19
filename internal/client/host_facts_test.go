package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func hostFactsFixtureValue() api.HostFacts {
	return api.HostFacts{Version: api.HostFactsVersion, HostID: strings.Repeat("a", 32), BootID: "opaque-boot-id",
		At: time.Unix(1, 0).UTC(), OS: "linux", Arch: "amd64", Level: "gvisor", LevelKnown: true,
		MemTotal: 8 << 30, MemForSessions: 6 << 30, MemReserved: 2 << 30, Room: 4 << 30, MemoryKnown: true, AccountingKnown: true,
		DiskFree: 32 << 30, DiskKnown: true, DrainKnown: true, WallsKnown: true,
		Walls: []api.WallProbe{{Driver: "gvisor", Level: "gvisor", CanPrepare: true, Version: "original version\noriginal spec", Facts: map[string]string{"construction": "known", "opaque": "first\nsecond"}}}}
}

func TestHostFactsV1KnownValidationPrecedesFutureExtensions(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		facts := hostFactsFixtureValue()
		if invalid {
			facts.Room++
		}
		raw, err := json.Marshal(facts)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"future":{"duplicate":1,"duplicate":2}}`)...)
		_, err = decodeHostFacts(raw, facts.HostID)
		var user *UserError
		want := "facts_future"
		if invalid {
			want = "facts_protocol"
		}
		if !errors.As(err, &user) || user.Code != want {
			t.Fatal("future extension hid or replaced the known V1 result", invalid, err)
		}
	}
	if _, err := decodeHostFacts([]byte(`{"v":2,"room":"opaque","MemReserved":false}`), strings.Repeat("a", 32)); err == nil {
		t.Fatal("unknown facts version became selectable")
	} else {
		var user *UserError
		if !errors.As(err, &user) || user.Code != "facts_future" {
			t.Fatal("unknown version interpreted known fields instead of staying opaque", err)
		}
	}
}

func TestHostFactsKnownOverReservationPreservesNegativeRoom(t *testing.T) {
	facts := hostFactsFixtureValue()
	facts.MemReserved = 7 << 30
	facts.Room = -1 << 30
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeHostFacts(raw, facts.HostID)
	if err != nil || got.Room != facts.Room || !got.AccountingKnown {
		t.Fatal("known native over-reservation was clamped or reclassified", got.Room, err)
	}
	for _, change := range []func(*api.HostFacts){
		func(f *api.HostFacts) { f.HostID = strings.Repeat("b", 32) },
		func(f *api.HostFacts) { f.MemoryKnown = false },
		func(f *api.HostFacts) { f.MemReserved = -1 },
	} {
		changed := cloneHostFacts(facts)
		change(&changed)
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeHostFacts(raw, facts.HostID); err == nil {
			t.Fatal("inconsistent native accounting became a supported observation")
		}
	}
}

func TestHostFactsActualReplyRetainsSourceWithoutSelectingGrantCandidate(t *testing.T) {
	h := h5GrantState(t)
	facts := hostFactsFixtureValue()
	c := finishGeneratedTransportFixture(t, h, facts.HostID, "facts")
	c.pending = map[uint64]chan proto.Response{}
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	calls := 0
	sharedGrantWire(t, c, func(request proto.Request) any {
		if request.Op != "host.facts" {
			t.Fatal("facts entered an application writer", request.Op)
		}
		calls++
		return facts
	})
	observed, err := c.readHostFacts(ctx)
	if err != nil || observed.receivedAt.IsZero() || calls != 1 {
		t.Fatal("actual facts reply lost its original receipt", err)
	}
	if r.selection != nil || r.operation != grantOpUnset {
		t.Fatal("candidate metadata prematurely selected a grant request")
	}
	view := cloneHostFacts(observed.facts)
	view.Walls[0].Facts["construction"] = "changed"
	if observed.facts.Walls[0].Facts["construction"] != "known" {
		t.Fatal("display clone rewrote the original observation")
	}
	if err := observed.check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := observed.check(ctx); err == nil {
		t.Fatal("identical remove/re-add renewed an old facts Source")
	}
}
