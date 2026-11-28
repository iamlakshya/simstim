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
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func TestSessionIdleDecideAndParseIdleStop(t *testing.T) {
	for _, off := range []string{"", "never", "  never  "} {
		d, err := ParseIdleStop(off)
		if err != nil || d != 0 {
			t.Errorf("ParseIdleStop(%q) = (%v, %v), want (0, nil)", off, d, err)
		}
	}
	for _, bad := range []string{"10s", "0s", "-5m", "soon"} {
		if _, err := ParseIdleStop(bad); err == nil {
			t.Errorf("ParseIdleStop(%q) unexpectedly succeeded", bad)
		}
	}
	if d, err := ParseIdleStop("30m"); err != nil || d != 30*time.Minute {
		t.Fatalf("ParseIdleStop(30m) = (%v, %v)", d, err)
	}

	t0 := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	// Off by default (after == 0) never stops.
	if _, stop := SessionIdleDecide(t0.Add(24*time.Hour), t0, false, 0); stop {
		t.Fatal("SessionIdleDecide with after=0 stopped session")
	}
	// Busy resets timer and never stops.
	busyAt, stop := SessionIdleDecide(t0.Add(10*time.Minute), t0, true, 5*time.Minute)
	if stop || !busyAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("busy: got (%v, %v)", busyAt, stop)
	}
	// Idle for less than after stays running.
	busyAt, stop = SessionIdleDecide(t0.Add(14*time.Minute), busyAt, false, 5*time.Minute)
	if stop {
		t.Fatal("stopped before idle_stop elapsed")
	}
	// Idle for >= after triggers stop.
	_, stop = SessionIdleDecide(t0.Add(15*time.Minute), busyAt, false, 5*time.Minute)
	if !stop {
		t.Fatal("did not stop once idle_stop elapsed")
	}
}

func TestMicroVMUnitMemoryLimitIncludesReserve(t *testing.T) {
	const guestRAM = 2 << 30 // 2 GiB
	mVM := &Manifest{
		Limits: Limits{MemoryMax: guestRAM},
		Wall:   &WallRecord{Level: api.LevelMicroVM, Driver: chv.DriverName},
	}
	if got, want := unitMemoryLimit(mVM), chv.UnitMemoryMax(guestRAM); got != want {
		t.Fatalf("microvm unitMemoryLimit = %d, want %d", got, want)
	}
	mProc := &Manifest{
		Limits: Limits{MemoryMax: guestRAM},
		Wall:   &WallRecord{Level: "process", Driver: "process"},
	}
	if got := unitMemoryLimit(mProc); got != guestRAM {
		t.Fatalf("process unitMemoryLimit = %d, want %d", got, guestRAM)
	}
}

func TestMicroVMLazyRestoreAfterReboot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	// Session 1: idle microvm from a previous boot -> lazy restore transitions
	// it cleanly to StateStopped without booting a VMM.
	mIdle := &Manifest{
		Schema:  1,
		ID:      "vmidle01",
		Name:    "idle-vm",
		Agent:   AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:     "/work",
		Limits:  Limits{MemoryMax: 1 << 30, CPUs: 2, DiskMax: chv.DefaultSessionDiskBytes},
		Run:     Runtime{BootID: "previous-boot-id", HolderPID: 99999, Unit: UnitName("vmidle01")},
		Status:  Status{State: StateIdle, Since: time.Now().UTC()},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Wall:    &WallRecord{Level: api.LevelMicroVM, Driver: chv.DriverName},
	}
	if err := mIdle.Save(); err != nil {
		t.Fatal(err)
	}

	// Session 2: explicitly stopped microvm -> stays skipped across reboot.
	mStopped := &Manifest{
		Schema:  1,
		ID:      "vmstop02",
		Name:    "stopped-vm",
		Agent:   AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:     "/work",
		Limits:  Limits{MemoryMax: 1 << 30, CPUs: 2, DiskMax: chv.DefaultSessionDiskBytes},
		Run:     Runtime{BootID: "previous-boot-id"},
		Status:  Status{State: StateStopped, Since: time.Now().UTC()},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Wall:    &WallRecord{Level: api.LevelMicroVM, Driver: chv.DriverName},
	}
	if err := mStopped.Save(); err != nil {
		t.Fatal(err)
	}

	results := Restore(false)
	byID := map[string]RestoreResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	if r := byID["vmidle01"]; r.Action != "stopped" || !strings.Contains(r.Why, "lazy restore") {
		t.Fatalf("vmidle01 restore = %+v, want Action=stopped lazy restore", r)
	}
	if r := byID["vmstop02"]; r.Action != "skipped" {
		t.Fatalf("vmstop02 restore = %+v, want Action=skipped", r)
	}

	reloaded, err := LoadManifest("vmidle01")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status.State != StateStopped || reloaded.Run.Unit != "" || reloaded.Run.HolderPID != 0 {
		t.Fatalf("reloaded idle microvm manifest = %+v, want StateStopped with cleared Run", reloaded)
	}
}

func TestNewMicroVMWalledValidationAndManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	origProbe := probeMicroVMWall
	defer func() { probeMicroVMWall = origProbe }()
	origAgentsDigest := resolveMicroVMAgentsDigest
	resolveMicroVMAgentsDigest = func() string { return "" }
	defer func() { resolveMicroVMAgentsDigest = origAgentsDigest }()
	t.Setenv("SHELL", "/bin/sh")

	// 1. Failing probe returns wall_unavailable with Hint.
	probeMicroVMWall = func(context.Context) (bool, string, string, string) {
		return false, "", "no /dev/kvm", "use process wall"
	}
	_, err := New(NewArgs{Name: "vm1", Agent: "shell", Wall: "microvm"})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_unavailable" || pe.Hint == "" {
		t.Fatalf("failing probe err = %v, want wall_unavailable", err)
	}

	// 2. Passing probe enforces minimum 16 MiB disk_max and checks stored image.
	probeMicroVMWall = func(context.Context) (bool, string, string, string) {
		return true, chv.PinnedVMMVersion, "", ""
	}
	_, err = New(NewArgs{Name: "vm1", Agent: "shell", Wall: "microvm", DiskMax: 4 << 20})
	if !errors.As(err, &pe) || pe.Code != "bad_disk_max" {
		t.Fatalf("tiny disk_max err = %v, want bad_disk_max", err)
	}
	_, err = New(NewArgs{Name: "vm1", Agent: "shell", Wall: "microvm"})
	if !errors.As(err, &pe) || pe.Code != "image_missing" {
		t.Fatalf("missing image err = %v, want image_missing", err)
	}

	// 3. With a stored rootfs image and runtime bundle, DeferStart builds a
	// complete microvm manifest with idle_stop off by default.
	platHex := strings.Repeat("c", 64)
	imgDir := filepath.Join(paths.ImagesDir(), platHex)
	if err := os.MkdirAll(imgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "rootfs.img"), []byte("ext4"), 0o444); err != nil {
		t.Fatal(err)
	}
	imgRec := api.ImageRecord{
		Ref:            "docker.io/library/buildpack-deps:trixie-scm",
		Digest:         "sha256:" + platHex,
		PlatformDigest: "sha256:" + platHex,
		Arch:           runtime.GOARCH,
		RootHash:       strings.Repeat("d", 64),
	}
	ib, _ := json.Marshal(imgRec)
	if err := os.WriteFile(filepath.Join(imgDir, "image.json"), ib, 0o600); err != nil {
		t.Fatal(err)
	}

	rp := chv.DefaultRuntimePaths()
	if err := os.MkdirAll(rp.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{rp.VMM, rp.CHRemote, rp.Kernel, filepath.Join(rp.Dir, "tools.erofs")} {
		if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tb, _ := json.Marshal(chv.ToolsRecord{Path: filepath.Join(rp.Dir, "tools.erofs"), RootHash: strings.Repeat("e", 64), DataBlocks: 64})
	if err := os.WriteFile(filepath.Join(rp.Dir, "tools.erofs.json"), tb, 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := New(NewArgs{
		Name:       "vm1",
		Agent:      "shell",
		Wall:       "microvm",
		MemoryMax:  1 << 30,
		DiskMax:    32 << 20,
		DeferStart: true,
	})
	if err != nil {
		t.Fatalf("New microvm DeferStart: %v", err)
	}
	m, err := LoadManifest(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Wall == nil || m.Wall.Level != api.LevelMicroVM || m.Wall.Driver != chv.DriverName {
		t.Fatalf("unexpected wall record: %+v", m.Wall)
	}
	if m.Wall.IdleStop != "" {
		t.Errorf("idle_stop must be off by default, got %q", m.Wall.IdleStop)
	}
	if m.Image == nil || m.Image.PlatformDigest != "sha256:"+platHex || m.Limits.DiskMax != 32<<20 {
		t.Fatalf("unexpected image/limits: image=%+v limits=%+v", m.Image, m.Limits)
	}
	if len(m.Agent.Argv) == 0 || m.Agent.Argv[0] != "/bin/sh" {
		t.Fatalf("microvm shell argv = %v, want [/bin/sh]", m.Agent.Argv)
	}

	// 4. Codex subscription auth (CODEX_AUTH_JSON) is detected both at New and
	// on StartDeferred (without calling addProcessCopyMount on chv).
	infoCodex, err := New(NewArgs{
		Name:       "vm-codex",
		Agent:      "codex",
		Wall:       "microvm",
		MemoryMax:  1 << 30,
		DiskMax:    32 << 20,
		DeferStart: true,
		Secrets:    map[string]api.Secret{"CODEX_AUTH_JSON": api.Secret(`{"auth_mode":"chatgpt"}`)},
	})
	if err != nil {
		t.Fatalf("New microvm codex DeferStart: %v", err)
	}
	mc, err := LoadManifest(infoCodex.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Wall == nil || mc.Wall.SubscriptionAuth != "secret:CODEX_AUTH_JSON" {
		t.Fatalf("microvm codex SubscriptionAuth = %q, want secret:CODEX_AUTH_JSON", mc.Wall.SubscriptionAuth)
	}
	if len(mc.Wall.BoundSecrets) != 1 || mc.Wall.BoundSecrets[0].Name != "CODEX_AUTH_JSON" {
		t.Fatalf("microvm codex BoundSecrets = %+v, want CODEX_AUTH_JSON", mc.Wall.BoundSecrets)
	}

	// Verify StartDeferred binds secrets and detects SubscriptionAuth on a
	// microvm manifest created with empty secrets at DeferStart time.
	infoDef, err := New(NewArgs{
		Name:       "vm-def",
		Agent:      "codex",
		Wall:       "microvm",
		MemoryMax:  1 << 30,
		DiskMax:    32 << 20,
		DeferStart: true,
	})
	if err != nil {
		t.Fatalf("New microvm deferred empty secrets: %v", err)
	}
	origStartDeferred := startDeferredSession
	var deferredManifest *Manifest
	startDeferredSession = func(m *Manifest, _ map[string]api.Secret) (*SessionInfo, error) {
		cp := *m
		deferredManifest = &cp
		if err := m.Save(); err != nil {
			return nil, err
		}
		return &SessionInfo{ID: m.ID}, nil
	}
	t.Cleanup(func() { startDeferredSession = origStartDeferred })
	if _, err := StartDeferred(api.SessionStartArgs{
		ID:      infoDef.ID,
		Secrets: map[string]api.Secret{"CODEX_AUTH_JSON": api.Secret(`{"auth_mode":"chatgpt"}`)},
	}); err != nil {
		t.Fatalf("StartDeferred microvm: %v", err)
	}
	if deferredManifest == nil || deferredManifest.Wall == nil || deferredManifest.Wall.SubscriptionAuth != "secret:CODEX_AUTH_JSON" {
		t.Fatalf("StartDeferred microvm SubscriptionAuth = %+v, want secret:CODEX_AUTH_JSON", deferredManifest)
	}
	if len(deferredManifest.Wall.BoundSecrets) != 1 || deferredManifest.Wall.BoundSecrets[0].Name != "CODEX_AUTH_JSON" {
		t.Fatalf("StartDeferred microvm BoundSecrets = %+v, want CODEX_AUTH_JSON", deferredManifest.Wall.BoundSecrets)
	}
	if len(deferredManifest.Wall.ExtraRW) != 0 {
		t.Fatalf("StartDeferred microvm ExtraRW = %+v, want none", deferredManifest.Wall.ExtraRW)
	}
}
