//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

func installTestGvisorRuntimeV1(t *testing.T, arch string) (supply.Manifest, []supply.Item) {
	t.Helper()
	release := "0.6.0"
	folder := supply.ReleaseFolder(release)
	roles := []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter", "runsc-fd-parking"}
	var items []supply.Item
	payloads := map[string][]byte{}
	for _, role := range roles {
		payload := []byte("#!/bin/sh\n# " + role + "\n")
		it := supply.NewItem(supply.KindGvisor, role, gvisor.NativeBuildVersionV1, arch, payload, folder+"/"+role+"-"+arch)
		it.Released = "2026-11-02"
		items = append(items, it)
		payloads[it.SHA256] = payload
	}
	manifest := supply.Manifest{
		Release:    release,
		Runtime:    "rt-gvisor-test-v1",
		Items:      items,
		Advisories: []supply.Advisory{},
	}
	imageDefaults := []supply.Item{{
		Kind:       supply.KindImage,
		Name:       "agent-base",
		Arch:       arch,
		Digest:     "sha256:" + strings.Repeat("a1", 32),
		RootHash:   strings.Repeat("b2", 32),
		DataBlocks: 64,
	}}

	ctx := t.Context()
	publication, store, err := openRuntimeProductionStore(ctx)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
		if publication != nil {
			_ = publication.close()
		}
	}()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range manifest.Items {
		part, err := store.BeginContext(publication.ctx, it)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.WriteAt(payloads[it.SHA256], 0); err != nil {
			_ = part.Close()
			t.Fatal(err)
		}
		if err := part.Finish(ctx); err != nil {
			_ = part.Close()
			t.Fatal(err)
		}
		_ = part.Close()
	}
	if err := commitRuntime(ctx, publication, store, manifest, &imageDefaults); err != nil {
		t.Fatal(err)
	}
	return manifest, imageDefaults
}

func TestSandboxPrepareSameConnectionImageDefaultsNeverFallBackToStore(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("release manifests support amd64 and arm64")
	}
	setupIsolatedPrepareHost(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gvisorManifest, imageDefaults := installTestGvisorRuntimeV1(t, runtime.GOARCH)

	// Confirm the retention store on disk holds ImageDefaults.
	onDiskDefaults, err := runtimeImageDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := imageDefaults[0].Digest
	if !onDiskDefaults[expectedDigest] {
		t.Fatalf("expected on-disk Store.ImageDefaults to contain %s", expectedDigest)
	}

	// Raw gvisor prepare without same-connection runtime selection must fail closed.
	bRaw := &bridge{ctx: ctx, hostID: "0123456789abcdef0123456789abcdef"}
	t.Cleanup(func() { bRaw.runtime.close() })
	rawArgs := api.SandboxPrepareArgsV1{Version: 1, Level: "gvisor", Runtime: &gvisorManifest}
	rawBytes, _ := json.Marshal(rawArgs)
	if _, err := bRaw.newSandboxPrepareWitnessV1(ctx, proto.Request{ID: 1, Op: "sandbox.prepare", Args: rawBytes}, rawBytes, rawArgs); err == nil {
		t.Fatal("expected raw gvisor prepare without same-connection runtime receipt to fail")
	}

	// Connection B remembers the runtime WITHOUT same-connection ImageDefaults:
	// defaultImageRequest MUST refuse rather than falling back to Store.ImageDefaults.
	bNoDefaults := &bridge{ctx: ctx, hostID: "0123456789abcdef0123456789abcdef"}
	t.Cleanup(func() { bNoDefaults.runtime.close() })
	if err := bNoDefaults.runtime.bindConnectionV1(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := bNoDefaults.runtime.status(ctx, runtimecache.RuntimeRequest{Manifest: gvisorManifest}); err != nil {
		t.Fatal(err)
	}
	receiptNoDefaults, err := bNoDefaults.runtime.selectedRuntime(ctx, gvisorManifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiptNoDefaults.defaultImageRequest(ctx, runtime.GOARCH); err == nil {
		t.Fatal("defaultImageRequest must refuse when same-connection ImageDefaults were omitted")
	} else {
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "runtime_origin" {
			t.Fatalf("expected runtime_origin refusal, got %v", err)
		}
	}

	// Connection C supplies same-connection ImageDefaults: defaultImageRequest
	// returns the exact release default image pin, and rejects conflicting updates.
	bWithDefaults := &bridge{ctx: ctx, hostID: "0123456789abcdef0123456789abcdef"}
	t.Cleanup(func() { bWithDefaults.runtime.close() })
	if err := bWithDefaults.runtime.bindConnectionV1(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := bWithDefaults.runtime.status(ctx, runtimecache.RuntimeRequest{Manifest: gvisorManifest, ImageDefaults: &imageDefaults}); err != nil {
		t.Fatal(err)
	}
	receiptWithDefaults, err := bWithDefaults.runtime.selectedRuntime(ctx, gvisorManifest)
	if err != nil {
		t.Fatal(err)
	}
	gotImage, err := receiptWithDefaults.defaultImageRequest(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if gotImage.Ref != brand.ImageRepository+"@"+expectedDigest ||
		gotImage.Digest != expectedDigest ||
		gotImage.PlatformDigest != expectedDigest ||
		gotImage.RootHash != imageDefaults[0].RootHash ||
		gotImage.DataBlocks != imageDefaults[0].DataBlocks {
		t.Fatalf("unexpected default image request: %+v", gotImage)
	}

	conflicting := []supply.Item{{
		Kind:       supply.KindImage,
		Name:       "agent-base",
		Arch:       runtime.GOARCH,
		Digest:     "sha256:" + strings.Repeat("c3", 32),
		RootHash:   strings.Repeat("d4", 32),
		DataBlocks: 128,
	}}
	if err := bWithDefaults.runtime.rememberRuntimeWithDefaults(ctx, gvisorManifest, &conflicting); err == nil {
		t.Fatal("expected conflicting same-connection ImageDefaults to fail closed")
	}

	// Witness construction on bWithDefaults succeeds for gvisor with matching Runtime.
	witness, err := bWithDefaults.newSandboxPrepareWitnessV1(ctx, proto.Request{ID: 2, Op: "sandbox.prepare", Args: rawBytes}, rawBytes, rawArgs)
	if err != nil {
		t.Fatalf("expected same-connection gvisor witness to succeed: %v", err)
	}
	if witness.driver != "gvisor" || !witness.captured || witness.runtime != receiptWithDefaults {
		t.Fatalf("unexpected gvisor witness: %+v", witness)
	}
}

func TestSandboxPrepareOneUseWitnessAdmission(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("release manifests support amd64 and arm64")
	}
	setupIsolatedPrepareHost(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &bridge{ctx: ctx, hostID: "0123456789abcdef0123456789abcdef"}

	raw := json.RawMessage(`{"version":1,"level":"process"}`)
	var args api.SandboxPrepareArgsV1
	_ = json.Unmarshal(raw, &args)
	witness, err := b.newSandboxPrepareWitnessV1(ctx, proto.Request{ID: 1, Op: "sandbox.prepare", Args: raw}, raw, args)
	if err != nil {
		t.Fatal(err)
	}

	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		reservation.close()
		t.Fatal(err)
	}
	m, err := buildSandboxPrepareManifestV1(ctx, reservation.id, "/usr/bin/true", witness)
	if err != nil {
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}

	// Bind and save once under placement lock.
	if err := bindAndSaveSandboxPrepareV1(operation, reservation, m, witness); err != nil {
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}
	if !operation.IsSandboxPrepareV1() {
		t.Fatal("expected operation.IsSandboxPrepareV1() after bind")
	}

	// Reusing the same one-use witness on a second reservation must fail closed.
	res2, err := reserveSessionID("")
	if err != nil {
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}
	op2, err := beginReservedSessionOperation(ctx, res2)
	if err != nil {
		res2.close()
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}
	m2, err := buildSandboxPrepareManifestV1(ctx, res2.id, "/usr/bin/true", witness)
	if err != nil {
		_ = cleanupSandboxPrepareSessionV1(op2, res2)
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}
	if err := bindAndSaveSandboxPrepareV1(op2, res2, m2, witness); err == nil {
		_ = cleanupSandboxPrepareSessionV1(op2, res2)
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal("expected second bind of one-use sandboxPrepareWitnessV1 to fail")
	}
	if err := cleanupSandboxPrepareSessionV1(op2, res2); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSandboxPrepareSessionV1(operation, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.SessionDir(reservation.id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected cleaned session directory %s to be absent", reservation.id)
	}
}
