package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPlacementCompletedThenStoppedUsesActualPreFactsFence(t *testing.T) {
	store, candidate := placementStoreFixture(t)
	const unit = int64(1 << 30)
	now := time.Now().UTC()
	record := placementRecordV1{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Host: candidate.host.Name, HostID: candidate.observation.hostID, Generation: candidate.observation.source.generation, At: now, Memory: unit, Session: "abcd2345", Reason: "awake"}
	if err := store.locked(t.Context(), "placements.json", func(ctx context.Context) error {
		return store.write(ctx, "placements.json", placementsV1{Version: 1, Records: []placementRecordV1{record}})
	}); err != nil {
		t.Fatal(err)
	}
	facts := overlapFactsFixtureV1(8*unit, 0, []api.MemoryChargeV1{})
	facts.HostID = record.HostID
	conn := finishGeneratedTransportFixture(t, candidate.host, record.HostID, "facts")
	conn.pending = map[uint64]chan proto.Response{}
	sharedGrantWire(t, conn, func(request proto.Request) any {
		if request.Op != "host.facts" {
			t.Fatal("fence read entered an application operation")
		}
		return facts
	})
	observed, fence, err := store.readFencedHostFactsV1(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	residual, err := placementInFlight([]placementRecordV1{record}, observed, now, fence)
	if err != nil || residual != 0 {
		t.Fatal("completed and already stopped start left phantom demand", residual, err)
	}
	residual, err = placementInFlight([]placementRecordV1{record}, observed, now)
	if err != nil || residual != unit {
		t.Fatal("missing causal fence was treated as permission", residual, err)
	}
	later, err := conn.readHostFacts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	residual, err = placementInFlight([]placementRecordV1{record}, later, now, fence)
	if err != nil || residual != unit {
		t.Fatal("an unrelated response adopted another observation's fence", residual, err)
	}
	changed := record
	changed.Memory = 2 * unit
	residual, err = placementInFlight([]placementRecordV1{changed}, observed, now, fence)
	if err != nil || residual != 2*unit {
		t.Fatal("changed record borrowed prior completion", residual, err)
	}
}

func TestPlacementPendingBoolHasClosedLifecycleShape(t *testing.T) {
	valid := `{"v":1,"records":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","host":"box","host_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","source_generation":"cccccccccccccccccccccccccccccccc","at":"2026-01-01T00:00:00Z","declared":1073741824,"reason":"awake","pending":true}]}`
	if _, err := decodePlacements([]byte(valid)); err == nil {
		t.Fatal("pending without actual prospective member was accepted")
	}
	for _, bad := range []string{"null", "1", "\"true\""} {
		withMember := strings.Replace(valid, "\"pending\":true", "\"session\":\"abcd2345\",\"pending\":"+bad, 1)
		if _, err := decodePlacements([]byte(withMember)); err == nil {
			t.Fatal("non-boolean lifecycle was accepted", bad)
		}
	}
}
