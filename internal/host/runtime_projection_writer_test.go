//go:build !windows

package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func runtimeOriginNewWriterFixtureV1(t *testing.T, b *bridge, full supply.Manifest, driver string, agents image.AgentDiskV1) (*SessionOperation, *sessionReservation, *Manifest) {
	t.Helper()
	selected, err := vmneed.SelectRuntimeForDriver(driver, full, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	need := vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: 256 << 20, Runtime: selected, Image: image.Request{Ref: "registry.example/runtime-origin:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: runtime.GOARCH, RootHash: strings.Repeat("c", 64), DataBlocks: 2}, Agents: agents.Content}, Driver: driver, CPUs: 2, DiskMax: 16 << 20}
	rawNeed, err := vmneed.EncodeV1(need, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	args := NewArgs{ID: id, Wall: "microvm", VMNeedV1: rawNeed, RuntimeOrigin: runtimeOriginRawFixtureV1(t, full)}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := b.newRequestContext(proto.Request{ID: 9, Op: "new", Args: raw}, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err = captureNewRequest(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveSessionID(id)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := operation.CloseWithError(); err != nil {
			t.Error(err)
		}
		reservation.close()
	})
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Schema: 2, ID: id, Name: "runtime-origin", Wall: &WallRecord{Level: "microvm", Driver: driver}, Limits: Limits{MemoryMax: budget.Limit, Declared: budget.Limit}, Agent: AgentInfo{Kind: "custom", Argv: []string{"true"}}}
	if err := admitReservedRuntime(ctx, operation, m, args); err != nil {
		t.Fatal(err)
	}
	return operation, reservation, m
}
func TestRuntimeOriginNewDurableWriterKeepsSelectedRuntimeOnlyV1(t *testing.T) {
	b, full, original, _ := runtimeOriginProducerFixtureV1(t)
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	operation, _, m := runtimeOriginNewWriterFixtureV1(t, b, full, "chv", agents)
	if err := saveReservedManifest(operation, m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(paths.SessionDir(m.ID), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("runtime_origin_v1")) {
		t.Fatal("origin data was persisted as authority")
	}
	saved, err := operation.LoadContext(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := vmneed.SelectRuntimeForDriver("chv", full, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RuntimeManifest == nil || len(saved.RuntimeManifest.Items) != 3 || !reflectRuntimeManifestEqualV1(*saved.RuntimeManifest, selected) {
		t.Fatal("durable New stored full collection or another selection")
	}
	current, err := b.runtime.selectedRuntime(b.ctx, full)
	if err != nil || current != original || len(original.files) != len(full.Items) {
		t.Fatal("durable New replaced original full producer", err)
	}
}
func TestRuntimeOriginNewWriterRefusesRetiredActualServiceBeforePublicationV1(t *testing.T) {
	b, full, _, _ := runtimeOriginProducerFixtureV1(t)
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	operation, _, m := runtimeOriginNewWriterFixtureV1(t, b, full, "chv", agents)
	if err := b.runtime.close(); err != nil {
		t.Fatal(err)
	}
	if err := saveReservedManifest(operation, m); err == nil {
		t.Fatal("retired actual service published a protected manifest")
	}
	if _, err := os.Stat(filepath.Join(paths.SessionDir(m.ID), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed writer left a manifest", err)
	}
}

// This uses the existing manifest-temp close fault seam, then calls the
// actual service closer while the real writer holds the receipt read lock.
func runtimeOriginRetireAtTempCloseV1(t *testing.T, b *bridge, prefix string) func() {
	t.Helper()
	original := closeManifestTemp
	done := make(chan error, 1)
	fired := false
	closeManifestTemp = func(file *os.File) error {
		err := original(file)
		if !strings.HasPrefix(filepath.Base(file.Name()), prefix) || fired {
			return err
		}
		fired = true
		go func() { done <- b.runtime.close() }()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			b.runtime.mu.Lock()
			closed := b.runtime.closed
			b.runtime.mu.Unlock()
			if closed {
				return err
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatal("actual service did not enter retirement")
			}
		}
	}
	return func() {
		closeManifestTemp = original
		if !fired {
			t.Error("writer did not reach the real pre-Rename seam")
			return
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}
func TestRuntimeOriginNewPreRenameRejectsServiceWaitingOnReceiptLockV1(t *testing.T) {
	b, full, _, _ := runtimeOriginProducerFixtureV1(t)
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	operation, _, m := runtimeOriginNewWriterFixtureV1(t, b, full, "chv", agents)
	finish := runtimeOriginRetireAtTempCloseV1(t, b, ".manifest-")
	err := saveReservedManifest(operation, m)
	finish()
	if err == nil {
		t.Fatal("New pre-Rename ignored retired service behind held receipt lock")
	}
	if _, err := os.Stat(filepath.Join(paths.SessionDir(m.ID), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired New writer published manifest", err)
	}
}
