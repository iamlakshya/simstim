package host

import (
	"bufio"
	"bytes"
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
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/testhome"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/vmneed"
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

func callSandboxPrepareDirect(t *testing.T, raw json.RawMessage) *proto.Error {
	t.Helper()
	var output bytes.Buffer
	b := &bridge{ctx: t.Context(), w: proto.NewWriter(&output)}
	b.handleSandboxPrepareV1(proto.Request{ID: 1, Op: "sandbox.prepare", Args: raw})
	respFrame, err := proto.Read(bufio.NewReader(&output))
	if err != nil || respFrame.Type != proto.TResponse {
		t.Fatalf("sandbox.prepare direct reply frame=%+v error=%v", respFrame, err)
	}
	var resp proto.Response
	if err := json.Unmarshal(respFrame.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected structured refusal for %s, got %+v", string(raw), resp)
	}
	return resp.Error
}

func TestSandboxPrepareWireValidation(t *testing.T) {
	setupIsolatedPrepareHost(t)

	cases := []struct {
		name   string
		raw    json.RawMessage
		code   string
		direct bool
	}{
		{name: "empty", raw: nil, code: "bad_request"},
		{name: "array", raw: json.RawMessage(`[]`), code: "bad_request"},
		{name: "unknown_field", raw: json.RawMessage(`{"version":1,"level":"process","command":"sh"}`), code: "bad_request"},
		{name: "trailing_json", raw: json.RawMessage(`{"version":1,"level":"process"}{}`), code: "bad_request", direct: true},
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
			var rpcErr *proto.Error
			if tc.direct {
				rpcErr = callSandboxPrepareDirect(t, tc.raw)
			} else {
				rpcErr = callSandboxPrepareOverBridge(t, tc.raw)
			}
			if rpcErr.Code != tc.code {
				t.Fatalf("got code %q, want %q (err=%+v)", rpcErr.Code, tc.code, rpcErr)
			}
			if strings.TrimSpace(rpcErr.Hint) == "" {
				t.Fatalf("missing Try hint on %+v", rpcErr)
			}
		})
	}
}

func TestSandboxPrepareFixedManifestQuotas(t *testing.T) {
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

	if err := cleanupSandboxPrepareSessionV1(operation, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.SessionDir(reservation.id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected cleaned session directory %s to be absent", reservation.id)
	}
}
