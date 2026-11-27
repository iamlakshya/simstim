//go:build linux

package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/testhome"
	"github.com/iamlakshya/simstim/internal/version"
)

func setupIsolatedPrepareHostLinux(t *testing.T) string {
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

	resetPlacementForTest := func() {
		placementProcesses.Lock()
		placementProcesses.entry = nil
		placementProcesses.byScope = map[string]*placement{}
		placementProcesses.Unlock()
	}
	resetPlacementForTest()
	t.Cleanup(resetPlacementForTest)

	uid := os.Getuid()
	cgroup := "0::/user.slice/user-" + strconvItoaTest(uid) + ".slice/session-1.scope\n"
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

func strconvItoaTest(v int) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func callBridgeSandboxPrepareLinux(t *testing.T, raw json.RawMessage) (api.SandboxPrepareResultV1, *proto.Error) {
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
	helloFrame, err := proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	var serverHello proto.Hello
	if err := json.Unmarshal(helloFrame.Payload, &serverHello); err != nil {
		t.Fatal(err)
	}
	if (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") && !slices.Contains(serverHello.Features, api.FeatureSandboxPrepareV1) {
		t.Fatalf("bridge features %v missing %q", serverHello.Features, api.FeatureSandboxPrepareV1)
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
	if respFrame.Type != proto.TResponse {
		t.Fatalf("unexpected frame type %v", respFrame.Type)
	}
	var resp proto.Response
	if err := json.Unmarshal(respFrame.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		if resp.Error == nil {
			t.Fatal("failed response carried nil error")
		}
		return api.SandboxPrepareResultV1{}, resp.Error
	}
	var result api.SandboxPrepareResultV1
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func TestSandboxPrepareLinuxSuccessAndNoHistoryIncrement(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)

	startedPath := filepath.Join(paths.StateDir(), "sandboxes-started")
	if err := os.WriteFile(startedPath, []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	unrelatedID := "ab23cd45"
	if err := makeSessionDir(unrelatedID); err != nil {
		t.Fatal(err)
	}
	unrelatedManifest := &Manifest{
		Schema:      1,
		ID:          unrelatedID,
		Name:        "keep-me",
		CreatedAt:   time.Now().UTC(),
		HostVersion: version.Version,
		Agent:       AgentInfo{Kind: "custom", Argv: []string{"/bin/true"}},
		Cwd:         "/tmp",
		Run:         Runtime{Socket: paths.Socket(unrelatedID)},
		Status:      Status{State: StateStopped, Since: time.Now().UTC()},
		Restart:     Restart{Policy: "resume", MaxAttempts: 3},
	}
	if err := unrelatedManifest.Save(); err != nil {
		t.Fatal(err)
	}
	unrelatedMarker := filepath.Join(paths.SessionDir(unrelatedID), "marker.txt")
	if err := os.WriteFile(unrelatedMarker, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}

	prevProbe := sandboxPrepareProbeOverrideV1
	prevBoot := sandboxPrepareBootOverrideV1
	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	var preparedID string
	sandboxPrepareBootOverrideV1 = func(ctx context.Context, operation *SessionOperation, reservation *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool) {
		preparedID = m.ID
		if !operation.IsSandboxPrepareV1() || !operation.Valid(m.ID) {
			t.Errorf("expected live sandbox-prepare SessionOperation for %s", m.ID)
		}
		loaded, err := operation.LoadContext(ctx)
		if err != nil {
			t.Errorf("LoadContext failed: %v", err)
		} else {
			if !IsSandboxPrepareManifestV1(loaded) {
				t.Errorf("saved manifest missing sandbox-prepare marker")
			}
			if loaded.Limits.MemoryMax != api.SandboxPrepareGuestRAMV1 || loaded.Limits.Declared != api.SandboxPrepareGuestRAMV1 {
				t.Errorf("unexpected lower-wall memory limits: %+v", loaded.Limits)
			}
			if loaded.Policy == nil || loaded.Policy.Sandbox.CPUs == nil || *loaded.Policy.Sandbox.CPUs != api.SandboxPrepareCPUsV1 {
				t.Errorf("unexpected CPU policy: %+v", loaded.Policy)
			}
			if loaded.Policy == nil || loaded.Policy.Sandbox.DiskMax == nil || *loaded.Policy.Sandbox.DiskMax != sandboxPrepareDiskStringV1 {
				t.Errorf("unexpected DiskMax policy: %+v", loaded.Policy)
			}
			if loaded.Restart.Policy != "never" {
				t.Errorf("unexpected restart policy %q", loaded.Restart.Policy)
			}
			if len(loaded.Agent.Argv) != 1 || filepath.Base(loaded.Agent.Argv[0]) != "true" {
				t.Errorf("unexpected fixed argv %v", loaded.Agent.Argv)
			}
		}
		return &sandboxPrepareHookResultV1{bootMS: 23}, true
	}
	t.Cleanup(func() {
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareBootOverrideV1 = prevBoot
	})

	res, rpcErr := callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	if res.Version != 1 || res.Level != "process" || res.Driver != "process" || res.BootMS != 23 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if preparedID == "" {
		t.Fatal("boot hook did not run")
	}
	for _, gone := range []string{paths.SessionDir(preparedID), paths.Socket(preparedID), paths.HookSocket(preparedID), paths.SandboxRoot(preparedID)} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s to be removed, stat err = %v", gone, err)
		}
	}

	afterStarted, err := os.ReadFile(startedPath)
	if err != nil || string(afterStarted) != "7\n" {
		t.Fatalf("sandboxes-started changed: %q, err=%v", string(afterStarted), err)
	}
	markerBytes, err := os.ReadFile(unrelatedMarker)
	if err != nil || string(markerBytes) != "untouched" {
		t.Fatalf("unrelated session altered: %q, err=%v", string(markerBytes), err)
	}
}

func TestSandboxPrepareLinuxBootFailurePositiveCleanup(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)

	prevProbe := sandboxPrepareProbeOverrideV1
	prevBoot := sandboxPrepareBootOverrideV1
	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	var failedID string
	sandboxPrepareBootOverrideV1 = func(_ context.Context, _ *SessionOperation, _ *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool) {
		failedID = m.ID
		return &sandboxPrepareHookResultV1{
			bootErr: UserError("wall_setup", "guest init failed to answer", "Run host doctor to inspect kernel namespaces."),
		}, true
	}
	t.Cleanup(func() {
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareBootOverrideV1 = prevBoot
	})

	_, rpcErr := callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr == nil {
		t.Fatal("expected sandbox_boot failure")
	}
	if rpcErr.Code != "sandbox_boot" || strings.TrimSpace(rpcErr.Hint) == "" {
		t.Fatalf("expected sandbox_boot with Try hint, got %+v", rpcErr)
	}
	if failedID == "" {
		t.Fatal("expected boot attempt reservation")
	}
	if _, err := os.Lstat(paths.SessionDir(failedID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed prepare session directory %s to be cleaned up, err=%v", failedID, err)
	}
}

func TestSandboxPrepareLinuxTimeoutCancellationAndCleanup(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)

	prevTimeout := sandboxPrepareTimeoutV1
	prevProbe := sandboxPrepareProbeOverrideV1
	prevBoot := sandboxPrepareBootOverrideV1
	sandboxPrepareTimeoutV1 = 80 * time.Millisecond
	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	timedOutIDCh := make(chan string, 1)
	observedCauseCh := make(chan error, 1)
	sandboxPrepareBootOverrideV1 = func(ctx context.Context, _ *SessionOperation, _ *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool) {
		timedOutIDCh <- m.ID
		<-ctx.Done()
		observedCauseCh <- ctx.Err()
		return &sandboxPrepareHookResultV1{bootErr: ctx.Err()}, true
	}
	t.Cleanup(func() {
		sandboxPrepareTimeoutV1 = prevTimeout
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareBootOverrideV1 = prevBoot
	})

	_, rpcErr := callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr == nil {
		t.Fatal("expected timeout failure")
	}
	var timedOutID string
	var observedCause error
	select {
	case timedOutID = <-timedOutIDCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prepare boot hook ID")
	}
	select {
	case observedCause = <-observedCauseCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prepare boot hook cancellation")
	}
	if !errors.Is(observedCause, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", observedCause)
	}
	if rpcErr.Code != "sandbox_boot" || strings.TrimSpace(rpcErr.Hint) == "" {
		t.Fatalf("expected sandbox_boot with Try hint, got %+v", rpcErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for liveSandboxPrepareRequestsV1.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Lstat(paths.SessionDir(timedOutID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected timed-out session directory %s to be cleaned up, err=%v", timedOutID, err)
	}
}

func TestSandboxPrepareLinuxClientLossCancellationAndCleanup(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)

	startedPath := filepath.Join(paths.StateDir(), "sandboxes-started")
	if err := os.WriteFile(startedPath, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	prevProbe := sandboxPrepareProbeOverrideV1
	prevBoot := sandboxPrepareBootOverrideV1
	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	inFlight := make(chan string, 1)
	cancelled := make(chan error, 1)
	sandboxPrepareBootOverrideV1 = func(ctx context.Context, _ *SessionOperation, _ *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool) {
		inFlight <- m.ID
		<-ctx.Done()
		cancelled <- ctx.Err()
		return &sandboxPrepareHookResultV1{bootErr: ctx.Err()}, true
	}
	t.Cleanup(func() {
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareBootOverrideV1 = prevBoot
	})

	clientConn, serverConn := net.Pipe()
	bridgeDone := make(chan error, 1)
	go func() {
		defer serverConn.Close()
		bridgeDone <- RunBridge(serverConn, serverConn)
	}()

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
		Args: json.RawMessage(`{"version":1,"level":"process"}`),
	}); err != nil {
		t.Fatal(err)
	}

	earlyReply := make(chan error, 1)
	go func() {
		f, err := proto.Read(reader)
		if err != nil {
			earlyReply <- err
			return
		}
		earlyReply <- fmt.Errorf("unexpected early bridge reply %d: %s", f.Type, string(f.Payload))
	}()

	var sessionID string
	select {
	case sessionID = <-inFlight:
	case err := <-earlyReply:
		_ = clientConn.Close()
		<-bridgeDone
		t.Fatalf("sandbox.prepare failed before boot started: %v", err)
	case <-time.After(10 * time.Second):
		_ = clientConn.Close()
		<-bridgeDone
		t.Fatal("timed out waiting for sandbox.prepare boot to start")
	}
	_ = clientConn.Close()

	cause := <-cancelled
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("expected Canceled on client loss, got %v", cause)
	}
	<-bridgeDone

	// Wait briefly for the request goroutine to finish synchronous cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(paths.SessionDir(sessionID)); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session directory %s remained after client-loss cleanup", sessionID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterStarted, err := os.ReadFile(startedPath)
	if err != nil || string(afterStarted) != "3\n" {
		t.Fatalf("sandboxes-started changed after client loss: %q, err=%v", string(afterStarted), err)
	}
}

func TestSandboxPrepareLinuxUnconfirmedCleanupRetainsOwner(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)
	previousTimeout := sandboxPrepareTimeoutV1
	sandboxPrepareTimeoutV1 = 2 * time.Second
	t.Cleanup(func() { sandboxPrepareTimeoutV1 = previousTimeout })
	settled := make(chan struct{})
	defer close(settled)

	prevProbe := sandboxPrepareProbeOverrideV1
	prevBoot := sandboxPrepareBootOverrideV1
	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	type retainedState struct {
		op  *SessionOperation
		res *sessionReservation
		id  string
	}
	retainedCh := make(chan retainedState, 1)
	sandboxPrepareBootOverrideV1 = func(_ context.Context, operation *SessionOperation, reservation *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool) {
		retainedCh <- retainedState{op: operation, res: reservation, id: m.ID}
		return &sandboxPrepareHookResultV1{
			bootErr:            errors.New("sandbox boot hung"),
			stopUnconfirmedErr: errors.New("native helper pidfd did not report exit"),
			stopConfirmed:      settled,
		}, true
	}
	var retained retainedState
	t.Cleanup(func() {
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareBootOverrideV1 = prevBoot
		if retained.res != nil {
			retained.res.close()
		}
		if retained.op != nil {
			retained.op.Close()
		}
		if retained.id != "" {
			_ = os.RemoveAll(paths.SessionDir(retained.id))
		}
	})

	_, rpcErr := callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr == nil {
		t.Fatal("expected sandbox_boot error when runtime exit is unconfirmed")
	}
	select {
	case retained = <-retainedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for retained prepare state")
	}
	if rpcErr.Code != "sandbox_boot" || !strings.Contains(rpcErr.Message, "physical cleanup is not yet confirmed") || strings.TrimSpace(rpcErr.Hint) == "" {
		t.Fatalf("expected explicit unconfirmed cleanup failure and Try hint, got %+v", rpcErr)
	}
	if liveSandboxPrepareRequestsV1.Load() == 0 {
		t.Fatal("bounded response dropped the original request worker")
	}
	if _, err := os.Stat(paths.Manifest(retained.id)); err != nil {
		t.Fatalf("expected retained manifest on disk: %v", err)
	}
	if retained.op == nil || !retained.op.Valid(retained.id) {
		t.Fatalf("expected retained SessionOperation to stay active and valid")
	}
	otherOp, busyErr := BeginSessionOperation(context.Background(), retained.id, 0)
	if otherOp != nil {
		otherOp.Close()
		t.Fatal("expected op.lock to remain held by the retained SessionOperation")
	}
	var pe *proto.Error
	if !errors.As(busyErr, &pe) || pe.Code != "busy" {
		t.Fatalf("expected busy lock refusal on retained session, got %v", busyErr)
	}
}

func TestSandboxPrepareLinuxUnavailableProbeAndLowDiskRefusals(t *testing.T) {
	setupIsolatedPrepareHostLinux(t)

	prevProbe := sandboxPrepareProbeOverrideV1
	prevDisk := sandboxPrepareDiskFreeV1
	t.Cleanup(func() {
		sandboxPrepareProbeOverrideV1 = prevProbe
		sandboxPrepareDiskFreeV1 = prevDisk
	})

	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error {
		return sandboxBootError("unprivileged user namespaces are restricted by AppArmor", "Run host prepare to install the root step.")
	}
	_, rpcErr := callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr == nil || rpcErr.Code != "sandbox_boot" || strings.TrimSpace(rpcErr.Hint) == "" {
		t.Fatalf("expected unavailable probe refusal with sandbox_boot and Try hint, got %+v", rpcErr)
	}

	sandboxPrepareProbeOverrideV1 = func(context.Context, *bridge, *sandboxPrepareWitnessV1) error { return nil }
	sandboxPrepareDiskFreeV1 = func(string) (int64, error) { return 512 << 20, nil }
	_, rpcErr = callBridgeSandboxPrepareLinux(t, json.RawMessage(`{"version":1,"level":"process"}`))
	if rpcErr == nil || rpcErr.Code != "sandbox_boot" || strings.TrimSpace(rpcErr.Hint) == "" {
		t.Fatalf("expected low-disk refusal with sandbox_boot and Try hint, got %+v", rpcErr)
	}

	entries, err := os.ReadDir(paths.SessionsDir())
	if err == nil && len(entries) != 0 {
		t.Fatalf("expected zero leftover session directories, got %d", len(entries))
	}
}

type fakeTrueFileInfo struct {
	name string
	mode fs.FileMode
	uid  uint32
}

func (f fakeTrueFileInfo) Name() string       { return f.name }
func (f fakeTrueFileInfo) Size() int64        { return 1 }
func (f fakeTrueFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeTrueFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeTrueFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeTrueFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestValidateSandboxPrepareTrueV1(t *testing.T) {
	if _, err := resolveSandboxPrepareTrueV1(); err != nil {
		t.Fatalf("resolveSandboxPrepareTrueV1 on host failed: %v", err)
	}

	rootExec := fakeTrueFileInfo{name: "true", mode: 0o755, uid: 0}
	rootLink := fakeTrueFileInfo{name: "true", mode: os.ModeSymlink | 0o777, uid: 0}
	cases := []struct {
		name         string
		bin          string
		binInfo      os.FileInfo
		resolved     string
		resolvedInfo os.FileInfo
		wantOK       bool
	}{
		{name: "regular usr bin true", bin: "/usr/bin/true", binInfo: rootExec, resolved: "/usr/bin/true", resolvedInfo: rootExec, wantOK: true},
		{name: "symlink to coreutils", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/coreutils", resolvedInfo: rootExec, wantOK: true},
		{name: "symlink to busybox", bin: "/bin/true", binInfo: rootLink, resolved: "/bin/busybox", resolvedInfo: rootExec, wantOK: true},
		{name: "symlink to gnu-true", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/lib/cargo/bin/coreutils/gnu-true", resolvedInfo: rootExec, wantOK: true},
		{name: "symlink to nix store", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/nix/store/abc-coreutils/bin/true", resolvedInfo: rootExec, wantOK: true},
		{name: "untrusted directory", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/tmp/true", resolvedInfo: rootExec, wantOK: false},
		{name: "unexpected basename", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/sh", resolvedInfo: rootExec, wantOK: false},
		{name: "bare -true suffix", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/-true", resolvedInfo: rootExec, wantOK: false},
		{name: "non-root symlink", bin: "/usr/bin/true", binInfo: fakeTrueFileInfo{name: "true", mode: os.ModeSymlink | 0o777, uid: 1000}, resolved: "/usr/bin/coreutils", resolvedInfo: rootExec, wantOK: false},
		{name: "non-root resolved", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/coreutils", resolvedInfo: fakeTrueFileInfo{name: "coreutils", mode: 0o755, uid: 1000}, wantOK: false},
		{name: "group-writable resolved", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/coreutils", resolvedInfo: fakeTrueFileInfo{name: "coreutils", mode: 0o775, uid: 0}, wantOK: false},
		{name: "world-writable bin", bin: "/usr/bin/true", binInfo: fakeTrueFileInfo{name: "true", mode: 0o757, uid: 0}, resolved: "/usr/bin/true", resolvedInfo: rootExec, wantOK: false},
		{name: "non-executable resolved", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/coreutils", resolvedInfo: fakeTrueFileInfo{name: "coreutils", mode: 0o644, uid: 0}, wantOK: false},
		{name: "symlink resolved info", bin: "/usr/bin/true", binInfo: rootLink, resolved: "/usr/bin/coreutils", resolvedInfo: rootLink, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSandboxPrepareTrueV1(tc.bin, tc.binInfo, tc.resolved, tc.resolvedInfo)
			if tc.wantOK && err != nil {
				t.Fatalf("expected acceptance, got %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("expected refusal, got nil")
			}
		})
	}
}
