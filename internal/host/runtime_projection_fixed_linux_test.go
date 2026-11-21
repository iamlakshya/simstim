//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func runtimeOriginFixedWitnessFixtureV1(t *testing.T) (*bridge, supply.Manifest, *selectedRuntimeReceiptV1, *sandboxPrepareWitnessV1, api.SandboxPrepareArgsV1) {
	t.Helper()
	var root image.Request
	b, full, original, _ := runtimeOriginProducerDefaultsFixtureV1(t, func(ctx context.Context) []supply.Item {
		root = runtimeOriginStoredRootFixtureV1(t, ctx)
		return []supply.Item{{Kind: supply.KindImage, Name: "agent-base", Arch: runtime.GOARCH, Digest: root.Digest, RootHash: root.RootHash, DataBlocks: root.DataBlocks}}
	})
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	selected, err := vmneed.SelectRuntimeForDriver("chv", full, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	need := vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: api.SandboxPrepareGuestRAMV1, Runtime: selected, Image: root, Agents: agents.Content}, Driver: "chv", CPUs: api.SandboxPrepareCPUsV1, DiskMax: api.SandboxPrepareDiskV1}
	rawNeed, err := vmneed.EncodeV1(need, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	args := api.SandboxPrepareArgsV1{Version: 1, Level: "microvm", Runtime: &selected, VMNeedV1: rawNeed, RuntimeOrigin: runtimeOriginRawFixtureV1(t, full)}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	witness, err := b.newSandboxPrepareWitnessV1(b.ctx, proto.Request{ID: 11, Op: "sandbox.prepare", Args: raw}, raw, args)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := witness.releaseImageLease(); err != nil {
			t.Error(err)
		}
	})
	return b, full, original, witness, args
}
func TestFixedPrepareRuntimeOriginKeepsLiveDefaultsAndSelectedDurableRuntimeV1(t *testing.T) {
	b, full, original, witness, args := runtimeOriginFixedWitnessFixtureV1(t)
	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(b.ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := operation.CloseWithError(); err != nil {
			t.Error(err)
		}
		reservation.close()
	})
	m, err := buildSandboxPrepareManifestV1(b.ctx, reservation.id, "", witness)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindAndSaveSandboxPrepareV1(operation, reservation, m, witness); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(paths.SessionDir(m.ID), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("runtime_origin_v1")) {
		t.Fatal("fixed prepare stored origin authority")
	}
	stored, err := operation.LoadContext(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RuntimeManifest == nil || len(stored.RuntimeManifest.Items) != 3 || !reflectRuntimeManifestEqualV1(*stored.RuntimeManifest, *args.Runtime) {
		t.Fatal("fixed prepare persisted the full collection")
	}
	current, err := b.runtime.selectedRuntime(b.ctx, full)
	if err != nil || current != original || len(original.files) != len(full.Items) {
		t.Fatal("fixed writer replaced original full producer", err)
	}
	// This separate actual bridge has no live defaults, although the disk store
	// still has the first request's pins. No on-disk fallback may authorise it.
	without, _, _ := runtimeOriginBridgeFixtureV1(t, full, nil)
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	_, err = without.newSandboxPrepareWitnessV1(without.ctx, proto.Request{ID: 12, Op: "sandbox.prepare", Args: encoded}, encoded, args)
	var refused *proto.Error
	if !errors.As(err, &refused) || refused.Code != "runtime_origin" || !strings.Contains(refused.Message, "default-image") {
		t.Fatal("fixed prepare borrowed on-disk defaults", err)
	}
}
func TestFixedPrepareRuntimeOriginWriterHonoursActualDeadlineAndRetirementV1(t *testing.T) {
	b, _, _, witness, _ := runtimeOriginFixedWitnessFixtureV1(t)
	canceled, cancel := context.WithCancel(b.ctx)
	cancel()
	if _, err := buildSandboxPrepareManifestV1(canceled, "abcd2345", "", witness); !errors.Is(err, context.Canceled) {
		t.Fatal("fixed manifest ignored original request cancellation", err)
	}
	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(b.ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operation.CloseWithError(); reservation.close() })
	m, err := buildSandboxPrepareManifestV1(b.ctx, reservation.id, "", witness)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.runtime.close(); err != nil {
		t.Fatal(err)
	}
	if err := bindAndSaveSandboxPrepareV1(operation, reservation, m, witness); err == nil {
		t.Fatal("retired fixed producer committed a manifest")
	}
	if _, err := os.Stat(filepath.Join(paths.SessionDir(m.ID), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed fixed writer published a manifest", err)
	}
}

func TestFixedPrepareOriginPreRenameRejectsServiceWaitingOnReceiptLockV1(t *testing.T) {
	b, _, _, witness, _ := runtimeOriginFixedWitnessFixtureV1(t)
	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(b.ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operation.CloseWithError(); reservation.close() })
	m, err := buildSandboxPrepareManifestV1(b.ctx, reservation.id, "", witness)
	if err != nil {
		t.Fatal(err)
	}
	finish := runtimeOriginRetireAtTempCloseV1(t, b, ".manifest-")
	err = bindAndSaveSandboxPrepareV1(operation, reservation, m, witness)
	finish()
	if err == nil {
		t.Fatal("fixed pre-Rename ignored service retirement behind held receipt lock")
	}
	if _, err := os.Stat(filepath.Join(paths.SessionDir(m.ID), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired fixed writer published manifest", err)
	}
}
