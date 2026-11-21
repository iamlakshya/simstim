//go:build !windows

package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// Format/byte fixtures only. No fixture VMM or kernel is executed. The actual
// store, status issuer, held descriptors and publication are exercised below.
func runtimeOriginEFIFormatV1(arch string) []byte {
	data := make([]byte, 1024)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], 0x80)
	copy(data[0x80:], "PE\x00\x00")
	machine := uint16(0x8664)
	if arch == "arm64" {
		machine = 0xaa64
		copy(data[0x38:], "ARMd")
		binary.LittleEndian.PutUint64(data[0x10:], 4096)
	} else {
		binary.LittleEndian.PutUint16(data[0x1fe:], 0xaa55)
		copy(data[0x202:], "HdrS")
		binary.LittleEndian.PutUint16(data[0x206:], 0x20b)
		data[0x211] = 1
	}
	header := data[0x84:]
	binary.LittleEndian.PutUint16(header, machine)
	binary.LittleEndian.PutUint16(header[2:], 1)
	binary.LittleEndian.PutUint16(header[16:], 112)
	binary.LittleEndian.PutUint16(header[18:], 2)
	optional := data[0x98:]
	binary.LittleEndian.PutUint16(optional, 0x20b)
	binary.LittleEndian.PutUint32(optional[16:], 0x1000)
	binary.LittleEndian.PutUint32(optional[32:], 4096)
	binary.LittleEndian.PutUint32(optional[36:], 512)
	binary.LittleEndian.PutUint32(optional[56:], 8192)
	binary.LittleEndian.PutUint32(optional[60:], 512)
	binary.LittleEndian.PutUint16(optional[68:], 10)
	section := data[0x98+112:]
	copy(section, ".text")
	binary.LittleEndian.PutUint32(section[8:], 512)
	binary.LittleEndian.PutUint32(section[12:], 0x1000)
	binary.LittleEndian.PutUint32(section[16:], 512)
	binary.LittleEndian.PutUint32(section[20:], 512)
	binary.LittleEndian.PutUint32(section[36:], 0x60000020)
	return data
}
func runtimeOriginFullFixtureV1(t *testing.T) (supply.Manifest, map[string][]byte) {
	t.Helper()
	arch, release := runtime.GOARCH, "1.0.0"
	folder := supply.ReleaseFolder(release) + "/"
	payloads := map[string][]byte{}
	makeItem := func(kind, name, version string, data []byte, url string) supply.Item {
		item := supply.NewItem(kind, name, version, arch, data, folder+url)
		payloads[item.SHA256] = data
		return item
	}
	chv := makeItem(supply.KindVMM, "cloud-hypervisor", "54.1", []byte("#!/bin/sh\nexit 0\n"), "cloud-hypervisor-static")
	chv.EOL, chv.Released, chv.HostKernel = "2027-01-06", "2026-11-11", ">=6.8"
	fc := makeItem(supply.KindVMM, "firecracker", "1.18.0", []byte("#!/bin/sh\n# firecracker fixture\nexit 0\n"), "firecracker-"+arch)
	fc.EOL, fc.Released, fc.HostKernel = "2027-01-06", "2026-11-11", "5.10,6.1,6.18"
	kernel := makeItem(supply.KindKernel, "guest", "6.18.63", []byte("kernel-format-not-executed"), supply.KernelAsset(arch))
	kernel.Licence, kernel.SourceURL, kernel.Released = "GPL-2.0-only", folder+supply.KernelSourceAsset(kernel.Version), "2026-11-10"
	efi := makeItem(supply.KindKernel, supply.KernelEFIName, "6.18.63", runtimeOriginEFIFormatV1(arch), supply.KernelEFIAsset(arch))
	efi.Licence, efi.SourceURL, efi.Released = kernel.Licence, kernel.SourceURL, kernel.Released
	data := bytes.Repeat([]byte("x"), 8192)
	tree, err := dmverity.MerkleTree(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tools := makeItem(supply.KindTools, "tools", release, slices.Concat(data, tree), supply.ToolsAsset(arch))
	tools.RootHash, tools.DataBlocks, tools.Salt = hex.EncodeToString(dmverity.RootHash(tree)), 2, strings.Repeat("0", 64)
	full := supply.Manifest{Release: release, Runtime: "original-full-fixture", Items: []supply.Item{chv, fc, kernel, efi, tools}, Advisories: []supply.Advisory{}}
	if err := runtimecache.Validate(full, arch); err != nil {
		t.Fatal(err)
	}
	return full, payloads
}
func runtimeOriginProducerFixtureV1(t *testing.T) (*bridge, supply.Manifest, *selectedRuntimeReceiptV1, func(string, any) proto.Response) {
	return runtimeOriginProducerDefaultsFixtureV1(t, nil)
}
func runtimeOriginProducerDefaultsFixtureV1(t *testing.T, makeDefaults func(context.Context) []supply.Item) (*bridge, supply.Manifest, *selectedRuntimeReceiptV1, func(string, any) proto.Response) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("actual bridge runtime producer requires Linux; portable carrier/selector tests remain separate")
	}
	setupIsolatedPrepareHost(t)
	full, payloads := runtimeOriginFullFixtureV1(t)
	ctx := t.Context()
	publication, store, err := openRuntimeProductionStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range full.Items {
		part, err := store.BeginContext(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.WriteAt(payloads[item.SHA256], 0); err != nil {
			t.Fatal(err)
		}
		if err := part.Finish(ctx); err != nil {
			t.Fatal(err)
		}
		if err := part.Close(); err != nil {
			t.Fatal(err)
		}
	}
	defaults := []supply.Item{}
	if makeDefaults != nil {
		defaults = makeDefaults(ctx)
	}
	if err := commitRuntime(ctx, store, full, &defaults); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publication.close(); err != nil {
		t.Fatal(err)
	}
	b, original, call := runtimeOriginBridgeFixtureV1(t, full, &defaults)
	return b, full, original, call
}
func runtimeOriginBridgeFixtureV1(t *testing.T, full supply.Manifest, defaults *[]supply.Item, allowMissing ...bool) (*bridge, *selectedRuntimeReceiptV1, func(string, any) proto.Response) {
	t.Helper()
	prior := map[*bridge]bool{}
	liveBridges.Range(func(key, value any) bool { prior[key.(*bridge)] = true; return true })
	clientConn, serverConn := net.Pipe()
	bridgeDone := make(chan error, 1)
	go func() { defer serverConn.Close(); bridgeDone <- RunBridge(serverConn, serverConn) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		if err := <-bridgeDone; err != nil {
			t.Error(err)
		}
	})
	reader := bufio.NewReader(clientConn) // one physical reader for this connection
	writer := proto.NewWriter(clientConn)
	if err := writer.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client"}); err != nil {
		t.Fatal(err)
	}
	helloFrame, err := proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	var hello proto.Hello
	if helloFrame.Type != proto.THello || json.Unmarshal(helloFrame.Payload, &hello) != nil {
		t.Fatal("actual bridge did not accept fixture hello")
	}
	var id uint64
	call := func(operation string, args any) proto.Response {
		id++
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: operation, Args: raw}); err != nil {
			t.Fatal(err)
		}
		frame, err := proto.Read(reader)
		if err != nil {
			t.Fatal(err)
		}
		var response proto.Response
		if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
			t.Fatal("actual bridge returned another response")
		}
		return response
	}
	response := call("runtime.status", runtimecache.RuntimeRequest{Manifest: full, ImageDefaults: defaults})
	if !response.OK {
		t.Fatal("actual bridge status issuer refused", response.Error)
	}
	var status api.RuntimeStatus
	if err := json.Unmarshal(response.Result, &status); err != nil {
		t.Fatal(err)
	}
	missing := false
	for _, item := range status.Items {
		if !item.Present {
			missing = true
		}
	}
	if missing && (len(allowMissing) == 0 || !allowMissing[0]) {
		t.Fatal("actual bridge did not verify every original runtime item")
	}

	var b *bridge
	liveBridges.Range(func(key, value any) bool {
		candidate := key.(*bridge)
		if !prior[candidate] && candidate.hostID == hello.HostID && candidate.ctx.Err() == nil {
			b = candidate
			return false
		}
		return true
	})
	if b == nil {
		t.Fatal("actual accepted bridge owner was not retained")
	}
	if missing {
		return b, nil, call
	}
	original, err := b.runtime.selectedRuntime(b.ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	if original.issuer != &b.runtime || len(original.files) != len(full.Items) {
		t.Fatal("status did not retain actual original producer")
	}
	return b, original, call
}

func TestRuntimeOriginUsesOnlyActualFullStatusProducerV1(t *testing.T) {
	b, full, original, _ := runtimeOriginProducerFixtureV1(t)
	before, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		selected, err := vmneed.SelectRuntimeForDriver(driver, full, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, driver, selected, nil); err == nil {
			t.Fatal("missing projected origin searched a full collection")
		}
		receipt, use, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, driver, selected, runtimeOriginRawFixtureV1(t, full))
		if err != nil {
			t.Fatal(driver, err)
		}
		if receipt != original || use.original != original || use.service != &b.runtime || len(original.manifest.Items) != 5 || len(original.files) != 5 {
			t.Fatal("projection fabricated or replaced an original producer")
		}
		got, err := selectedRuntimeInventoryV1(b.ctx, receipt, use)
		if err != nil || !reflectRuntimeManifestEqualV1(got, selected) {
			t.Fatal("projection inventory differs", err)
		}
		original.mu.RLock()
		err = checkSelectedRuntimeManifestLockedV1(b.ctx, receipt, use, &selected)
		original.mu.RUnlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := json.Marshal(original.manifest)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("selection rewrote original full producer metadata", err)
	}
}
func runtimeOriginRawFixtureV1(t *testing.T, full supply.Manifest) json.RawMessage {
	t.Helper()
	raw, err := api.EncodeRuntimeOriginV1(api.RuntimeOriginV1{V: 1, Manifest: full})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func reflectRuntimeManifestEqualV1(a, b supply.Manifest) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
func TestRuntimeOriginRoleDigestOwnerCancellationAndRetirementRefuseV1(t *testing.T) {
	b, full, original, _ := runtimeOriginProducerFixtureV1(t)
	selected, err := vmneed.SelectRuntimeForDriver("chv", full, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	carrier := runtimeOriginRawFixtureV1(t, full)
	_, use, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, "chv", selected, carrier)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"role", "digest", "full-origin", "selected-order", "full-order", "duplicate-role"} {
		expected := cloneSelectedRuntime(selected)
		originValue, _ := api.DecodeRuntimeOriginV1(carrier)
		origin := api.CloneRuntimeOriginV1(originValue)
		switch kind {
		case "role":
			expected.Items[0].Name = "firecracker"
		case "digest":
			expected.Items[0].SHA256 = strings.Repeat("b", 64)
		case "full-origin":
			origin.Manifest.Runtime = "another-origin"
		case "selected-order":
			expected.Items[0], expected.Items[1] = expected.Items[1], expected.Items[0]
		case "full-order":
			origin.Manifest.Items[0], origin.Manifest.Items[1] = origin.Manifest.Items[1], origin.Manifest.Items[0]
		case "duplicate-role":
			origin.Manifest.Items = append(origin.Manifest.Items, origin.Manifest.Items[0])
		}
		hostile, err := json.Marshal(origin)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, "chv", expected, hostile); err == nil {
			t.Fatal("hostile projection data supplied an original producer", kind)
		}
	}
	other := &bridge{ctx: b.ctx, hostID: b.hostID}
	if err := other.runtime.bindConnectionV1(other.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.runtime.close() })
	if _, _, err := other.runtime.selectedVMRuntimeOriginV1(other.ctx, "chv", selected, carrier); err == nil {
		t.Fatal("another service borrowed original source")
	}
	// Negative copied-map attack only; no positive fixture inserts a receipt.
	key, err := selectedRuntimeKey(full)
	if err != nil {
		t.Fatal(err)
	}
	other.runtime.selections = map[string]*selectedRuntimeReceiptV1{key: original}
	if _, _, err := other.runtime.selectedVMRuntimeOriginV1(other.ctx, "chv", selected, carrier); err == nil {
		t.Fatal("copied map data replaced original issuer")
	}
	other.runtime.selections = nil
	canceled, cancel := context.WithCancel(b.ctx)
	cancel()
	original.mu.RLock()
	err = checkSelectedRuntimeManifestLockedV1(canceled, original, use, &selected)
	original.mu.RUnlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("writer lost cancellation", err)
	}
	if err := b.runtime.close(); err != nil {
		t.Fatal(err)
	}
	original.mu.RLock()
	err = use.checkActiveLocked(b.ctx)
	original.mu.RUnlock()
	if err == nil {
		t.Fatal("retired producer view authorised a writer")
	}
}

// This real store/tar/ext4/pin fixture is not installed-agent discovery, jail
// or VM proof. It supplies the independent actual held agents producer needed
// by the New runtime consumer; no receipt or map entry is fabricated.
func runtimeOriginAgentsPinFixtureV1(t *testing.T, b *bridge) image.AgentDiskV1 {
	t.Helper()
	publication, err := beginRuntimePublication(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := image.OpenHost(b.ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(publication.owner.control)
	capture, err := captureInstalledAgents(b.ctx, home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	disk, lease, err := store.EnsureAgentsV1(b.ctx, runtime.GOARCH, func(ctx context.Context, previous string, out *os.File) (image.AgentsBuildResultV1, error) {
		source, err := image.AgentsSourceDigestV1(ctx, capture.inputs, capture.binaries)
		result := image.AgentsBuildResultV1{SourceDigest: source, Agents: capture.kinds, Reused: previous != "" && previous == source}
		if err != nil || result.Reused {
			return result, err
		}
		var raw bytes.Buffer
		if _, err := image.WriteAgentsTarV1(ctx, capture.inputs, capture.binaries, &raw); err != nil {
			return result, err
		}
		return result, image.ConvertAgentsV1(ctx, &raw, out)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publication.close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := PinInstalledAgentsV1(b.ctx, disk.Content)
	if err != nil {
		t.Fatal(err)
	}
	b.agents.retainAgentsReceiptV1(receipt)
	t.Cleanup(func() {
		if err := b.agents.close(); err != nil {
			t.Error(err)
		}
	})
	return disk
}
func TestNewRuntimeProjectionConsumerKeepsActualStatusAndAgentsOwnersV1(t *testing.T) {
	b, full, original, _ := runtimeOriginProducerFixtureV1(t)
	disk := runtimeOriginAgentsPinFixtureV1(t, b)
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		selected, err := vmneed.SelectRuntimeForDriver(driver, full, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		need := vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: 1 << 30, Runtime: selected, Image: image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: runtime.GOARCH, RootHash: strings.Repeat("c", 64), DataBlocks: 2}, Agents: disk.Content}, Driver: driver, CPUs: 2, DiskMax: 16 << 20}
		rawNeed, err := vmneed.EncodeV1(need, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		a := NewArgs{ID: "abcd2345", Wall: "microvm", VMNeedV1: rawNeed, RuntimeOrigin: runtimeOriginRawFixtureV1(t, full)}
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		ctx, err := b.newRequestContext(proto.Request{ID: 5, Op: "new", Args: raw}, a)
		if err != nil {
			t.Fatal(driver, err)
		}
		ctx, _, err = captureNewRequest(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
		if witness == nil || witness.runtime != original || witness.runtimeUse == nil || witness.runtimeUse.original != original || witness.agents == nil || !witness.captured {
			t.Fatal("actual New did not bind the original status/agents producers")
		}
		witness.runtime.mu.RLock()
		witness.agents.mu.RLock()
		err = withPlacement(ctx, false, func(control *placementControl) error { return witness.checkProducersLocked(ctx, control) }, false)
		witness.agents.mu.RUnlock()
		witness.runtime.mu.RUnlock()
		if err != nil {
			t.Fatal("actual New lost original producers at protected writer gate", driver, err)
		}
	}
}

func TestRuntimeOriginCanceledProducerRequiresNewActualStatusIssuerV1(t *testing.T) {
	b, full, old, call := runtimeOriginProducerFixtureV1(t)
	selected, err := vmneed.SelectRuntimeForDriver("chv", full, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	carrier := runtimeOriginRawFixtureV1(t, full)
	_, priorUse, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, "chv", selected, carrier)
	if err != nil {
		t.Fatal(err)
	}
	old.publication.cancel(errors.New("original producer cancelled"))
	old.mu.RLock()
	err = checkSelectedRuntimeManifestLockedV1(b.ctx, old, priorUse, &selected)
	old.mu.RUnlock()
	if err == nil {
		t.Fatal("writer renewed a cancelled original producer")
	}
	defaults := []supply.Item{}
	response := call("runtime.status", runtimecache.RuntimeRequest{Manifest: full, ImageDefaults: &defaults})
	if !response.OK {
		t.Fatal("new actual bridge status did not settle and reverify retired producer", response.Error)
	}
	current, use, err := b.runtime.selectedVMRuntimeOriginV1(b.ctx, "chv", selected, carrier)
	if err != nil {
		t.Fatal(err)
	}
	if current == old || use.original != current || current.issuer != &b.runtime {
		t.Fatal("actual issuer revived a retired object instead of verifying a new owner")
	}
	old.mu.RLock()
	err = priorUse.checkActiveLocked(b.ctx)
	old.mu.RUnlock()
	if err == nil {
		t.Fatal("old private use switched to the new owner by equal metadata")
	}
}
