package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func withPrepareTestRoute(ctx context.Context, rt route) context.Context {
	return context.WithValue(ctx, hostPrepareTestRouteKey{}, rt)
}

func TestHostPrepareFlowPhasesUseDistinctConnectionsAndPublishFloor(t *testing.T) {
	hostID := strings.Repeat("d", 32)
	h := Host{Name: "box", SSH: "box.example"}
	target, _ := prepareSavedFixture(t, h, TierOwn, hostID, true)

	var probeCalls, prepareCalls atomic.Int32
	tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		switch req.Op {
		case "sandbox.probe":
			probeCalls.Add(1)
			p := prepareGVisorProbe()
			p.Linger = false
			p.IOURingDisabled = 0
			return p, nil, false
		case "sandbox.prepare":
			prepareCalls.Add(1)
			return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 18}, nil, false
		default:
			return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
		}
	})
	ctx := withPrepareTestRoute(t.Context(), route{t: tr})

	probe, mode, err := ProbeHostPrepareV1(ctx, target, "", false, false)
	if err != nil {
		t.Fatalf("Phase 1 probe failed: %v", err)
	}
	if mode != "auto" || probe.OS != "linux" {
		t.Fatalf("Phase 1 got mode=%q os=%q", mode, probe.OS)
	}
	afterPhase1 := tr.starts.Load()
	if afterPhase1 != 1 {
		t.Fatalf("Phase 1 started %d connections, want 1", afterPhase1)
	}

	if _, err := DeliverHostPrepareRuntimeV1(ctx, target, "", false, nil, nil); err != nil {
		t.Fatalf("Phase 2 runtime failed: %v", err)
	}

	var shown RootScript
	rootOut, err := RunHostPrepareRootPhaseV1(ctx, target, HostPrepareRootOptionsV1{
		ExplicitMode: RootSkip,
		ShowScript:   func(s RootScript) { shown = s },
	})
	if err != nil {
		t.Fatalf("Phase 3 root skip failed: %v", err)
	}
	if !rootOut.Needed || !rootOut.Skipped || rootOut.Mode != RootSkip || shown.SHA256 == "" {
		t.Fatalf("unexpected Phase 3 outcome: %+v", rootOut)
	}
	afterPhase3 := tr.starts.Load()
	if afterPhase3 <= afterPhase1 {
		t.Fatalf("Phase 3 reused earlier connection: starts=%d afterPhase1=%d", afterPhase3, afterPhase1)
	}

	outcome, saved, err := BootAndRecordHostPrepareV1(ctx, target, "", HostPrepareOptionsV1{}, false, false, nil)
	if err != nil {
		t.Fatalf("Phase 4 boot+record failed: %v", err)
	}
	if tr.starts.Load() <= afterPhase3 {
		t.Fatalf("Phase 4 did not open its own connection: starts=%d afterPhase3=%d", tr.starts.Load(), afterPhase3)
	}
	if !outcome.Tested() || outcome.Result().Level != "gvisor" || outcome.Result().BootMS != 18 {
		t.Fatalf("unexpected Phase 4 outcome: tested=%v res=%+v", outcome.Tested(), outcome.Result())
	}
	if saved.WallFloor != "gvisor" || saved.Prepared == nil || saved.Prepared.Level != "gvisor" {
		t.Fatalf("saved host missing floor/cache: %+v prepared=%+v", saved, saved.Prepared)
	}
	reloaded, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WallFloor != "gvisor" || reloaded.Prepared == nil || reloaded.Prepared.Level != "gvisor" {
		t.Fatalf("hosts.toml did not persist floor/cache: %+v", reloaded)
	}
	records, err := RootStepRecords(t.Context(), reloaded)
	if err != nil || len(records) != 1 || records[0].Status != "skipped" {
		t.Fatalf("root-steps.json records = %+v, err = %v", records, err)
	}
}

func TestHostPrepareFlowRefusesABARemoveReaddBetweenPhases(t *testing.T) {
	hostID := strings.Repeat("e", 32)
	h := Host{Name: "box", SSH: "box.example"}
	target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)

	var prepareCalls atomic.Int32
	tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		switch req.Op {
		case "sandbox.probe":
			return prepareGVisorProbe(), nil, false
		case "sandbox.prepare":
			prepareCalls.Add(1)
			return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 10}, nil, false
		default:
			return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
		}
	})
	ctx := withPrepareTestRoute(t.Context(), route{t: tr})

	if _, _, err := ProbeHostPrepareV1(ctx, target, "", false, false); err != nil {
		t.Fatalf("initial probe failed: %v", err)
	}
	startsAfterProbe := tr.starts.Load()

	// Simulate remove + re-add of the same host name between phases.
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierOwn, hostID, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := DeliverHostPrepareRuntimeV1(ctx, target, "", false, nil, nil); err == nil {
		t.Fatal("Phase 2 accepted removed-and-readded ABA host")
	}
	if _, err := RunHostPrepareRootPhaseV1(ctx, target, HostPrepareRootOptionsV1{ExplicitMode: RootSkip}); err == nil {
		t.Fatal("Phase 3 accepted removed-and-readded ABA host")
	}
	if _, _, err := BootAndRecordHostPrepareV1(ctx, target, "", HostPrepareOptionsV1{}, false, false, nil); err == nil {
		t.Fatal("Phase 4 accepted removed-and-readded ABA host")
	}
	if tr.starts.Load() != startsAfterProbe {
		t.Fatalf("ABA phases dialed %d extra connections after remove/re-add", tr.starts.Load()-startsAfterProbe)
	}
	if prepareCalls.Load() != 0 {
		t.Fatalf("sandbox.prepare ran %d times on ABA host", prepareCalls.Load())
	}
	readded, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if readded.WallFloor != "" || readded.Prepared != nil {
		t.Fatalf("re-added host mutated by stale preparation target: %+v", readded)
	}
}

func TestHostPrepareFlowBootFailureAndPrintedNoWallPreserveState(t *testing.T) {
	t.Run("boot_failure_leaves_floor_and_cache_unchanged", func(t *testing.T) {
		hostID := strings.Repeat("1", 32)
		prevAt := nowUTC().Add(-2 * time.Hour)
		h := Host{
			Name:      "box",
			SSH:       "box.example",
			WallFloor: "process",
			Prepared:  &PreparedHost{Level: "process", Arch: "amd64", MemTotal: 4 << 30, PreparedAt: prevAt},
		}
		target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)

		tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
			switch req.Op {
			case "sandbox.probe":
				return prepareGVisorProbe(), nil, false
			case "sandbox.prepare":
				return nil, &proto.Error{Code: "sandbox_boot", Message: "guest init failed", Hint: "Run host doctor."}, false
			default:
				return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
			}
		})
		ctx := withPrepareTestRoute(t.Context(), route{t: tr})
		if _, _, err := BootAndRecordHostPrepareV1(ctx, target, "", HostPrepareOptionsV1{}, false, false, nil); err == nil {
			t.Fatal("expected failed test boot to return an error")
		}
		got, err := FindHost("box")
		if err != nil {
			t.Fatal(err)
		}
		if got.WallFloor != "process" || got.Prepared == nil || got.Prepared.Level != "process" || !got.Prepared.PreparedAt.Equal(prevAt) {
			t.Fatalf("failed boot mutated floor or cache: %+v prepared=%+v", got, got.Prepared)
		}
	})

	t.Run("printed_root_with_no_runnable_wall_leaves_floor_empty", func(t *testing.T) {
		t.Chdir(t.TempDir())
		hostID := strings.Repeat("2", 32)
		h := Host{Name: "box", SSH: "box.example"}
		target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)

		noWallProbe := api.CapProbe{
			OS:               "linux",
			Arch:             "amd64",
			Kernel:           "6.8.0",
			User:             "dev",
			UID:              1000,
			MemTotal:         8 << 30,
			LandlockABI:      3,
			Seccomp:          true,
			UserNSRestricted: true,
			HasSetfacl:       true,
			KVM:              api.KVM{Present: true, Usable: false, Group: "kvm"},
			Walls: []api.WallProbe{
				{Driver: "process", Level: "process", OK: false, Reason: "AppArmor restricts user namespaces", Facts: map[string]string{"root_blocker": "apparmor_userns"}},
			},
		}
		var prepareCalls atomic.Int32
		tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
			switch req.Op {
			case "sandbox.probe":
				return noWallProbe, nil, false
			case "sandbox.prepare":
				prepareCalls.Add(1)
				return api.SandboxPrepareResultV1{}, nil, false
			default:
				return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
			}
		})
		ctx := withPrepareTestRoute(t.Context(), route{t: tr})

		rootOut, err := RunHostPrepareRootPhaseV1(ctx, target, HostPrepareRootOptionsV1{ExplicitMode: RootPrint})
		if err != nil {
			t.Fatalf("Phase 3 print failed: %v", err)
		}
		if !rootOut.Printed || rootOut.ScriptFile == "" {
			t.Fatalf("expected printed root script, got %+v", rootOut)
		}
		info, err := os.Stat(rootOut.ScriptFile)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("script file stat = %v, err = %v", info, err)
		}

		outcome, saved, err := BootAndRecordHostPrepareV1(ctx, target, "", HostPrepareOptionsV1{RootPrinted: rootOut.Printed}, false, false, nil)
		if err != nil {
			t.Fatalf("Phase 4 no-wall returned unexpected error: %v", err)
		}
		if !outcome.NoRunnableWall() || outcome.Tested() || !outcome.RootPrinted() || outcome.LevelAfterRoot() != "process" {
			t.Fatalf("unexpected no-wall outcome: noWall=%v tested=%v rootPrinted=%v afterRoot=%q",
				outcome.NoRunnableWall(), outcome.Tested(), outcome.RootPrinted(), outcome.LevelAfterRoot())
		}
		if prepareCalls.Load() != 0 {
			t.Fatalf("sandbox.prepare called %d times when no runnable wall existed", prepareCalls.Load())
		}
		if saved.WallFloor != "" || saved.Prepared != nil {
			t.Fatalf("no-wall outcome wrote floor or cache: %+v", saved)
		}
		got, err := FindHost("box")
		if err != nil {
			t.Fatal(err)
		}
		if got.WallFloor != "" || got.Prepared != nil {
			t.Fatalf("hosts.toml gained floor or cache on no-wall outcome: %+v", got)
		}
	})
}

func TestHostPrepareFlowPreservesStrongerExistingWallFloorAndImagesFlag(t *testing.T) {
	hostID := strings.Repeat("3", 32)
	h := Host{Name: "box", SSH: "box.example", WallFloor: "microvm"}
	target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)

	var seenReleaseURL string
	tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		switch req.Op {
		case "sandbox.probe":
			var args struct {
				ReleaseURL string `json:"release_url"`
			}
			_ = json.Unmarshal(req.Args, &args)
			seenReleaseURL = args.ReleaseURL
			return prepareGVisorProbe(), nil, false
		case "sandbox.prepare":
			return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 14}, nil, false
		default:
			return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
		}
	})
	ctx := withPrepareTestRoute(t.Context(), route{t: tr})

	var ue *UserError
	if _, _, err := ProbeHostPrepareV1(ctx, target, "bogus", false, false); !errors.As(err, &ue) || ue.Code != "bad_images_fetch" || ue.Kind != KindUsage {
		t.Fatalf("expected bad_images_fetch usage error, got %v", err)
	}
	if tr.starts.Load() != 0 {
		t.Fatalf("invalid --images dialed %d times", tr.starts.Load())
	}

	if _, mode, err := ProbeHostPrepareV1(ctx, target, "laptop", false, false); err != nil || mode != "laptop" || seenReleaseURL != "" {
		t.Fatalf("laptop probe got mode=%q releaseURL=%q err=%v", mode, seenReleaseURL, err)
	}

	outcome, saved, err := BootAndRecordHostPrepareV1(ctx, target, "laptop", HostPrepareOptionsV1{Level: "gvisor"}, false, false, nil)
	if err != nil {
		t.Fatalf("BootAndRecordHostPrepareV1 failed: %v", err)
	}
	if !outcome.Tested() || saved.WallFloor != "microvm" || saved.Prepared == nil || saved.Prepared.Level != "gvisor" {
		t.Fatalf("stronger floor not preserved: outcome.Tested=%v floor=%q prepared=%+v", outcome.Tested(), saved.WallFloor, saved.Prepared)
	}
}
