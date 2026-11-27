//go:build linux

package microvm

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
)

func TestCheckpointActorCannotOriginateFromPlanOrLiteralPhaseV3(t *testing.T) {
	server := &runnerServer{}
	for _, request := range []*checkpointActorRequestV3{nil, {Version: 3, Action: "probe"}, {Version: 3, Action: "create", Metadata: checkpoint.Metadata{AgentSessionIDs: map[string]string{}}}} {
		reply := server.checkpointOperationV3(t.Context(), request)
		if reply == nil || reply.Error == nil {
			t.Fatal("checkpoint data created an original native actor")
		}
	}
	backend := &subordinateCheckpointBackendV3{}
	if err := backend.Probe(t.Context()); err == nil {
		t.Fatal("a literal backend issued native storage")
	}
	if (&NativeBindingV1{}).HasSubordinateCheckpointBackendV3() {
		t.Fatal("a zero engine binding selected a subordinate actor")
	}
	if _, err := backend.Select(t.Context(), "20260101T000000.000Z"); err == nil {
		t.Fatal("the running actor invented independent snapshot custody")
	}
}

// This is actual rooted storage and FICLONE, not native actor acceptance.
// Outside Source authority is tested separately and cannot come from this
// fixture's literal server fields or ordinary current-process credentials.
func TestCheckpointActorFixedWriterUsesActualOwnedRootV3(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	parent, err := owner.vm.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	server := &runnerServer{readonlyDisk: &readonlyControlDiskV2{root: owner.vm, disk: owner.disk, parent: parent, session: "fixed", driver: "chv", bytes: owner.bytes}}
	t.Cleanup(func() { _ = server.closeCheckpointActorV3() })
	probe := server.checkpointOperationV3(t.Context(), &checkpointActorRequestV3{Version: 3, Action: "probe"})
	if probe.Error != nil {
		if probe.Error.Code == "rewind_off" {
			t.Skip("this actual test filesystem cannot FICLONE")
		}
		t.Fatal(probe.Error)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	created := server.checkpointOperationV3(t.Context(), &checkpointActorRequestV3{Version: 3, Action: "create", When: at, Metadata: checkpoint.Metadata{AgentSessionIDs: map[string]string{"session": "original"}}, Keep: 2})
	if created.Error != nil || created.Entry == nil || !created.Entry.Time.Equal(at) {
		t.Fatal("the fixed actual actor did not create its checkpoint", created.Error)
	}
	listed := server.checkpointOperationV3(t.Context(), &checkpointActorRequestV3{Version: 3, Action: "list"})
	if listed.Error != nil || listed.Entry == nil || !listed.Entry.Time.Equal(at) {
		t.Fatal("the actual actor did not list its created pair", listed.Error)
	}
	discard := server.checkpointOperationV3(t.Context(), &checkpointActorRequestV3{Version: 3, Action: "discard", Entry: created.Entry})
	if discard.Error != nil {
		t.Fatal(discard.Error)
	}
	listed = server.checkpointOperationV3(t.Context(), &checkpointActorRequestV3{Version: 3, Action: "list"})
	if listed.Error != nil || listed.Entry != nil {
		t.Fatal("actual creator cleanup left a complete pair", listed.Error)
	}
}
