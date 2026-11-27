//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/checkpoint"
)

func TestSlotSnapshotUnissuedStoppedAndPreparedReject(t *testing.T) {
	var backend slotSnapshotStoppedV2
	if err := backend.Probe(context.Background()); err == nil {
		t.Fatal("unissued backend probed native storage")
	}
	var prepared slotSnapshotPreparedV2
	if err := prepared.Commit(context.Background()); err == nil {
		t.Fatal("data committed an actual slot replacement")
	}
	if err := prepared.Rollback(context.Background()); err == nil {
		t.Fatal("data rolled back an actual slot replacement")
	}
	if err := prepared.Finalize(context.Background()); err == nil {
		t.Fatal("data finalized an actual slot replacement")
	}
	if value := prepared.Journal(); value.Version != 0 {
		t.Fatal("unissued prepared data returned an original transaction")
	}
	if _, err := backend.PrepareRestore(context.Background(), nil, checkpoint.TransactionV1{}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("a callback issued a stopped replacement")
	}
}

// These are refusal-only objects, with no native source or execution grant.
// They exercise the cancellation ordering that must preserve SAME-owner retry.
func TestSlotSnapshotRefusedClosePreservesPreparedCleanupLifetime(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := &SlotSnapshotOwnerV1{lifetime: lifetime, cancel: cancel}
	owner.issuer = owner
	backendLifetime, backendCancel := context.WithCancel(context.Background())
	defer backendCancel()
	backend := &slotSnapshotStoppedV2{owner: owner, lifetime: backendLifetime, cancel: backendCancel}
	backend.issuer = backend
	backend.prepared = &slotSnapshotPreparedV2{}
	owner.backend = backend
	if err := owner.Close(); err == nil {
		t.Fatal("unsettled prepared owner was discarded")
	}
	if lifetime.Err() != nil || backendLifetime.Err() != nil || backend.closed || backend.closing || owner.backend != backend || backend.prepared == nil {
		t.Fatal("refused Close cancelled or dropped the actual cleanup continuation")
	}
}
