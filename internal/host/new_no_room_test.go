//go:build !windows

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This fixture exercises the actual reserved directory and retained OP. Its
// controlled room verdict is not evidence of native memory accounting.
func newNoRoomOperationFixtureV1(t *testing.T) (*sessionReservation, *SessionOperation) {
	t.Helper()
	agentBridgeEnv(t)
	args := NewArgs{ID: "abcd2345", Agent: "shell", Wall: "process"}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &bridge{ctx: t.Context(), hostID: strings.Repeat("a", 32)}
	ctx, err := bridge.newRequestContext(proto.Request{ID: 7, Op: "new", Args: raw, NewAttempt: strings.Repeat("b", 32)}, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err = captureNewRequest(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveSessionID(args.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		reservation.close()
		t.Fatal(err)
	}
	if err := bindNewNoRoomAttemptV1(ctx, operation, reservation); err != nil {
		reservation.close()
		operation.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { reservation.close(); operation.Close() })
	return reservation, operation
}

func TestNewNoRoomSettlementFollowsActualTerminalCleanup(t *testing.T) {
	reservation, operation := newNoRoomOperationFixtureV1(t)
	refusal := newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: true, Needed: 2, Room: 1})
	pending := sealNewNoRoomBeforeDispatchV1(operation, reservation, refusal)
	var proof *pendingNewNoRoomV1
	if !errors.As(pending, &proof) || checkNewNoRoomTerminalV1(operation) == nil {
		t.Fatal("actual refusal did not freeze future dispatch")
	}
	if err := saveReservedManifest(operation, &Manifest{ID: reservation.id}); err == nil {
		t.Fatal("a sealed operation wrote another manifest")
	}
	if !operation.active.Load() {
		t.Fatal("the refusal pretended native closure before cleanup")
	}
	result := finishReservedNewNoRoomV1(operation, reservation, pending)
	var wire *proto.Error
	if !errors.As(result, &wire) || wire.Code != "no_room" {
		t.Fatal(result)
	}
	envelope, err := api.DecodeNewNoRoomEnvelopeV1(wire.Data)
	if err != nil || envelope.Settlement.RequestID != 7 || operation.active.Load() {
		t.Fatal("settlement did not follow actual native close", err)
	}
	if _, err := os.Stat(paths.SessionDir(reservation.id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("positive cleanup left the refused reservation", err)
	}
}

func TestNewNoRoomPublicOrForeignRefusalCannotSealTheOperation(t *testing.T) {
	reservation, operation := newNoRoomOperationFixtureV1(t)
	defer reservation.close()
	for _, failure := range []error{
		UserError("no_room", "a matching public code", "Choose another host."),
		newNativeRoomRefusalFromGateV1(&SessionOperation{}, memoryRoomFactsV1{Known: true, Needed: 2, Room: 1}),
		newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: false, Needed: 2, Room: 1}),
		newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: true, Needed: 1, Room: 2}),
	} {
		var proof *pendingNewNoRoomV1
		if result := sealNewNoRoomBeforeDispatchV1(operation, reservation, failure); errors.As(result, &proof) || operation.newNoRoom.terminal.Load() {
			t.Fatal("metadata created primary dispatch closure")
		}
	}
}

func TestNewNoRoomChangedReservationDoesNotDeleteTheReplacement(t *testing.T) {
	reservation, operation := newNoRoomOperationFixtureV1(t)
	pending := sealNewNoRoomBeforeDispatchV1(operation, reservation, newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: true, Needed: 2, Room: 1}))
	original := paths.SessionDir(reservation.id)
	if err := os.Rename(original, original+"-kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(original, "keep")
	if err := os.WriteFile(marker, []byte("another owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := finishReservedNewNoRoomV1(operation, reservation, pending)
	var wire *proto.Error
	if !errors.As(result, &wire) || wire.Code != "start_cleanup" || len(wire.Data) != 0 {
		t.Fatal("changed cleanup received a settlement", result)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "another owner" {
		t.Fatal("cleanup deleted a replacement reservation", err)
	}
}

func TestNewNoRoomSettlementWaitsForTheOriginalOperationReader(t *testing.T) {
	reservation, operation := newNoRoomOperationFixtureV1(t)
	entered, release, readerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		_ = operation.withCurrentRead(t.Context(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original reader did not begin")
	}
	pending := sealNewNoRoomBeforeDispatchV1(operation, reservation, newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: true, Needed: 2, Room: 1}))
	result := make(chan error, 1)
	go func() { result <- finishReservedNewNoRoomV1(operation, reservation, pending) }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for operation.active.Load() {
		select {
		case <-deadline.C:
			close(release)
			t.Fatal("actual operation close did not begin")
		default:
			runtime.Gosched()
		}
	}
	select {
	case <-result:
		close(release)
		t.Fatal("settlement preceded its original reader join")
	default:
	}
	close(release)
	select {
	case err := <-result:
		var wire *proto.Error
		if !errors.As(err, &wire) {
			t.Fatal(err)
		}
		if _, err := api.DecodeNewNoRoomEnvelopeV1(wire.Data); err != nil {
			t.Fatal(err)
		}
	case <-deadline.C:
		t.Fatal("settlement did not follow actual native close")
	}
	<-readerDone
}

func TestNewNoRoomNeverDispatchedProofRejectsAnActualStartupOwner(t *testing.T) {
	reservation, operation := newNoRoomOperationFixtureV1(t)
	defer reservation.close()
	operation.startup = &startupLaunchV1{}
	result := sealNewNoRoomBeforeDispatchV1(operation, reservation, newNativeRoomRefusalFromGateV1(operation, memoryRoomFactsV1{Known: true, Needed: 2, Room: 1}))
	var pending *pendingNewNoRoomV1
	if errors.As(result, &pending) || operation.newNoRoom.terminal.Load() {
		t.Fatal("a startup owner acquired never-dispatched proof")
	}
}
