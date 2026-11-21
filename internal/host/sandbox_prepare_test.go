package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/testhome"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

func setupIsolatedPrepareHost(t *testing.T) string {
	t.Helper()
	root, err := testhome.ShortTempDir("sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	home := filepath.Join(root, "home")
	runDir := filepath.Join(root, "run")
	configDirs := filepath.Join(root, "config-dirs")
	dataDirs := filepath.Join(root, "data-dirs")
	for _, dir := range []string{home, runDir, configDirs, dataDirs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_CONFIG_DIRS", configDirs)
	t.Setenv("XDG_DATA_DIRS", dataDirs)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("SHELL", "/bin/sh")

	resetPlacement := func() {
		placementProcesses.Lock()
		placementProcesses.entry = nil
		placementProcesses.byScope = map[string]*placement{}
		placementProcesses.Unlock()
	}
	resetPlacement()
	t.Cleanup(resetPlacement)

	uid := os.Getuid()
	cgroup := "0::/user.slice/user-" + strconv.Itoa(uid) + ".slice/session-1.scope\n"
	prevSelf, prevNS := SelfCgroup, CgroupNamespaces
	SelfCgroup = func() ([]byte, error) { return []byte(cgroup), nil }
	CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
	t.Cleanup(func() {
		SelfCgroup = prevSelf
		CgroupNamespaces = prevNS
	})

	if err := InitPlacement(); err != nil {
		t.Fatal(err)
	}
	return root
}

func callSandboxPrepareOverBridge(t *testing.T, raw json.RawMessage) *proto.Error {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	bridgeDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		bridgeDone <- RunBridge(serverConn, serverConn)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		<-bridgeDone
	})

	reader := bufio.NewReader(clientConn)
	writer := proto.NewWriter(clientConn)
	if err := writer.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "client",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Read(reader); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteJSON(proto.TRequest, proto.Request{
		ID:   1,
		Op:   "sandbox.prepare",
		Args: raw,
	}); err != nil {
		t.Fatal(err)
	}
	respFrame, err := proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	if err := json.Unmarshal(respFrame.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatalf("expected refusal for %s, got success %s", string(raw), string(resp.Result))
	}
	if resp.Error == nil {
		t.Fatal("expected structured error on refused sandbox.prepare")
	}
	return resp.Error
}

func installTestGvisorRuntimeV1(t *testing.T, arch string) (supply.Manifest, []supply.Item) {
	t.Helper()
	release := "0.6.0"
	folder := supply.ReleaseFolder(release)
	roles := []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter"}
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

	ctx := context.Background()
	publication, store, err := openRuntimeProductionStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = store.Close()
		_ = publication.close()
	}()
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
	if err := commitRuntime(ctx, store, manifest, &imageDefaults); err != nil {
		t.Fatal(err)
	}
	return manifest, imageDefaults
}

func TestSandboxPrepareWireValidation(t *testing.T) {
	setupIsolatedPrepareHost(t)

	cases := []struct {
		name string
		raw  json.RawMessage
		code string
	}{
		{name: "empty", raw: nil, code: "bad_request"},
		{name: "array", raw: json.RawMessage(`[]`), code: "bad_request"},
		{name: "unknown_field", raw: json.RawMessage(`{"version":1,"level":"process","command":"sh"}`), code: "bad_request"},
		{name: "trailing_json", raw: json.RawMessage(`{"version":1,"level":"process"}{}`), code: "bad_request"},
		{name: "version_zero", raw: json.RawMessage(`{"version":0,"level":"process"}`), code: "bad_request"},
		{name: "version_two", raw: json.RawMessage(`{"version":2,"level":"process"}`), code: "bad_request"},
		{name: "level_none", raw: json.RawMessage(`{"version":1,"level":"none"}`), code: "wall_unavailable"},
		{name: "level_unknown", raw: json.RawMessage(`{"version":1,"level":"docker"}`), code: "bad_request"},
		{name: "process_with_vm_need", raw: json.RawMessage(`{"version":1,"level":"process","vm_need_v1":{"version":1}}`), code: "bad_request"},
		{name: "microvm_without_need", raw: json.RawMessage(`{"version":1,"level":"microvm"}`), code: "runtime_origin"},
		{name: "microvm_bad_need", raw: json.RawMessage(`{"version":1,"level":"microvm","vm_need_v1":{"version":1}}`), code: "bad_request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rpcErr := callSandboxPrepareOverBridge(t, tc.raw)
			if rpcErr.Code != tc.code {
				t.Fatalf("got code %q, want %q (err=%+v)", rpcErr.Code, tc.code, rpcErr)
			}
			if strings.TrimSpace(rpcErr.Hint) == "" {
				t.Fatalf("missing Try hint on %+v", rpcErr)
			}
		})
	}
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
	if err := bNoDefaults.runtime.rememberRuntime(ctx, gvisorManifest); err != nil {
		t.Fatal(err)
	}
	receiptNoDefaults, err := bNoDefaults.runtime.selectedRuntime(gvisorManifest)
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
	if err := bWithDefaults.runtime.rememberRuntimeWithDefaults(ctx, gvisorManifest, &imageDefaults); err != nil {
		t.Fatal(err)
	}
	receiptWithDefaults, err := bWithDefaults.runtime.selectedRuntime(gvisorManifest)
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

func TestSandboxPrepareFixedManifestQuotasAndOneUseWitness(t *testing.T) {
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

	m, err := buildSandboxPrepareManifestV1(reservation.id, "/usr/bin/true", witness)
	if err != nil {
		_ = cleanupSandboxPrepareSessionV1(operation, reservation)
		t.Fatal(err)
	}
	if !IsSandboxPrepareManifestV1(m) {
		t.Fatal("expected sandbox-prepare manifest marker")
	}
	if m.Limits.MemoryMax != api.SandboxPrepareGuestRAMV1 || m.Limits.Declared != api.SandboxPrepareGuestRAMV1 {
		t.Fatalf("unexpected process memory limits: %+v", m.Limits)
	}
	if m.Policy == nil || m.Policy.Preset != api.PresetLocked || m.Policy.Unknown != api.UnknownDeny ||
		m.Policy.Sandbox.CPUs == nil || *m.Policy.Sandbox.CPUs != api.SandboxPrepareCPUsV1 ||
		m.Policy.Sandbox.DiskMax == nil || *m.Policy.Sandbox.DiskMax != sandboxPrepareDiskStringV1 {
		t.Fatalf("unexpected fixed policy: %+v", m.Policy)
	}
	if !reflect.DeepEqual(m.Agent.Argv, []string{"/usr/bin/true"}) || m.Restart.Policy != "never" {
		t.Fatalf("unexpected fixed command or restart: argv=%v restart=%+v", m.Agent.Argv, m.Restart)
	}

	// Verify microvm budget calculation matches 512 MiB guest + canonical reserve (642 MiB).
	vmBudget, err := vmneed.BudgetFor(api.SandboxPrepareGuestRAMV1)
	if err != nil || vmBudget.Guest != 512<<20 || vmBudget.Limit <= vmBudget.Guest {
		t.Fatalf("unexpected VM budget for 512 MiB: %+v, err=%v", vmBudget, err)
	}

	// Any violation of fixed invariants must fail validateSandboxPrepareBootManifestV1.
	badRestart := *m
	badRestart.Restart = Restart{Policy: "resume"}
	if err := validateSandboxPrepareBootManifestV1(&badRestart); err == nil {
		t.Fatal("expected resume restart policy to be rejected")
	}
	badCmd := *m
	badCmd.Agent.Argv = []string{"/bin/sh"}
	if err := validateSandboxPrepareBootManifestV1(&badCmd); err == nil {
		t.Fatal("expected non-true command to be rejected")
	}
	badMem := *m
	badMem.Limits.MemoryMax = 1 << 30
	if err := validateSandboxPrepareBootManifestV1(&badMem); err == nil {
		t.Fatal("expected altered memory cap to be rejected")
	}
	badSecrets := *m
	badSecrets.Secrets = []string{"OPENAI_API_KEY"}
	if err := validateSandboxPrepareBootManifestV1(&badSecrets); err == nil {
		t.Fatal("expected secrets on prepare manifest to be rejected")
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
	m2, err := buildSandboxPrepareManifestV1(res2.id, "/usr/bin/true", witness)
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
