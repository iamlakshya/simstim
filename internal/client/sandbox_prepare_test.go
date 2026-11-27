package client

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func prepareSavedFixture(t *testing.T, h Host, tier Tier, hostID string, withPin bool) (*HostPrepareTargetV1, hostKey) {
	t.Helper()
	stateFDTestHome(t)
	key := prepareTestHostKey(7)
	if withPin {
		staged, abort, err := StagePinOwned(t.Context(), h.Name)
		if err != nil {
			t.Fatal(err)
		}
		defer abort()
		st, err := stageForContext(staged, h.Name)
		if err != nil || st == nil {
			t.Fatalf("stage lookup: %v", err)
		}
		if err := writePin(h.Name, st, []hostKey{key}); err != nil {
			t.Fatal(err)
		}
		change := HostChange{Kind: "host add", Now: &h, Pin: true, Tier: tier, HostID: hostID}
		obs, err := ObserveHostRegistrationContext(staged, change)
		if err != nil {
			t.Fatal(err)
		}
		target, err := UpdateHostsForPreparation(WithHostStateObservation(staged, obs), change)
		if err != nil {
			t.Fatal(err)
		}
		return target, key
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, tier, hostID, ""); err != nil {
		t.Fatal(err)
	}
	target, err := OpenHostPrepareTargetV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	return target, key
}

func TestSandboxPreparePurposeAndCallbackMixingRejected(t *testing.T) {
	hostID := strings.Repeat("c", 32)
	h := Host{Name: "box", SSH: "box.example"}
	target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)

	var prepareCalls atomic.Int32
	tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1, "session.id", "secrets"}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		switch req.Op {
		case "sandbox.probe":
			return prepareGVisorProbe(), nil, false
		case "sandbox.prepare":
			prepareCalls.Add(1)
			return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 12}, nil, false
		default:
			return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
		}
	})
	conn, err := target.dialRoute(t.Context(), route{t: tr})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Wrong or absent GrantRequest purposes cannot enter PrepareSandboxV1.
	for _, tc := range []struct {
		name string
		req  *GrantRequest
	}{
		{"nil", nil},
		{"new", NewGrantRequest(false, false)},
		{"root", NewRootStepGrantRequest()},
		{"push", NewPushGrantRequest()},
		{"runtime-upgrade", &GrantRequest{purpose: grantRuntimeUpgrade}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.req != nil {
				defer tc.req.Close()
				ctx = WithGrantRequest(ctx, tc.req)
			}
			if _, err := conn.PrepareSandboxV1(ctx, target, HostPrepareOptionsV1{}); err == nil {
				t.Fatalf("purpose %s entered PrepareSandboxV1", tc.name)
			}
		})
	}

	// Raw sandbox.prepare wire call without owned plan must be refused before frame write.
	rawReq := NewHostPrepareGrantRequest()
	defer rawReq.Close()
	rawCtx := WithGrantRequest(t.Context(), rawReq)
	var rawRes api.SandboxPrepareResultV1
	if err := conn.Call(rawCtx, "sandbox.prepare", api.SandboxPrepareArgsV1{Version: 1, Level: "gvisor"}, &rawRes); err == nil {
		t.Fatal("raw sandbox.prepare call bypassed owned plan guard")
	}
	if prepareCalls.Load() != 0 {
		t.Fatalf("refused callers reached sandbox.prepare on the wire %d times", prepareCalls.Load())
	}

	// Host-prepare purpose cannot donate to session start, root step or outbound/ledger grants.
	if _, _, err := conn.beginGrant(rawCtx, "new", true); err == nil {
		t.Fatal("grantHostPrepare donated to ordinary session beginGrant")
	}
	if err := conn.CheckNewGrants(rawCtx, nil, false); err == nil {
		t.Fatal("grantHostPrepare donated to CheckNewGrants")
	}
	script, err := BuildRootScript("dev", 1000, []RootStepID{RootLinger}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.CheckRootStepGrant(rawCtx, script, RootPrint, false); err == nil {
		t.Fatal("grantHostPrepare donated to CheckRootStepGrant")
	}
	if err := allowRequestGrant(rawCtx, h, hostID, TierOwn, Outbound{Kind: OutboundSecret, Name: "API_KEY"}, DefaultSettings()); err == nil {
		t.Fatal("grantHostPrepare donated to OutboundSecret")
	}
	if err := recordRequestGrant(rawCtx, h, hostID, TierOwn, Outbound{Kind: OutboundRootRun, Name: script.SHA256}); err == nil {
		t.Fatal("grantHostPrepare donated to grant ledger recording")
	}

	// Valid prepare succeeds, then another GrantRequest cannot consume its outcome or imagePhaseV1.
	prepReq := NewHostPrepareGrantRequest()
	defer prepReq.Close()
	prepCtx := WithGrantRequest(t.Context(), prepReq)
	outcome, err := conn.PrepareSandboxV1(prepCtx, target, HostPrepareOptionsV1{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepReq.sandboxPrepare.imagePhaseV1(prepCtx, conn); err == nil {
		t.Fatal("imagePhaseV1 opened outside active VM/image preparation phase")
	}
	otherReq := NewHostPrepareGrantRequest()
	defer otherReq.Close()
	if _, err := RecordSandboxPreparationV1(WithGrantRequest(t.Context(), otherReq), target, outcome); err == nil {
		t.Fatal("a different GrantRequest recorded another plan's preparation outcome")
	}
}

func TestSandboxPrepareFailureCleanupProtocolAndNoWallPreserveFloor(t *testing.T) {
	basePrepared := &PreparedHost{
		Level:      "gvisor",
		Arch:       "amd64",
		MemTotal:   4 << 30,
		PreparedAt: nowUTC().Add(-time.Hour),
	}
	hostID := strings.Repeat("d", 32)

	cases := []struct {
		name        string
		tier        Tier
		features    []string
		probe       api.CapProbe
		opts        HostPrepareOptionsV1
		reply       func(proto.Request) (any, *proto.Error, bool)
		expectNoWal bool
	}{
		{
			name:     "boot-failed",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return nil, &proto.Error{Code: "sandbox_boot", Message: "test boot failed"}, false
			},
		},
		{
			name:     "cleanup-uncertain",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return nil, &proto.Error{Code: "cleanup_uncertain", Message: "could not confirm test sandbox removal"}, false
			},
		},
		{
			name:     "connection-lost-mid-boot",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return nil, nil, true
			},
		},
		{
			name:     "extra-json-field",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return json.RawMessage(`{"version":1,"level":"gvisor","driver":"gvisor","boot_ms":10,"extra":true}`), nil, false
			},
		},
		{
			name:     "wrong-level-reply",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return api.SandboxPrepareResultV1{Version: 1, Level: "process", Driver: "process", BootMS: 10}, nil, false
			},
		},
		{
			name:     "negative-boot-ms",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			reply: func(proto.Request) (any, *proto.Error, bool) {
				return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: -1}, nil, false
			},
		},
		{
			name:     "missing-feature",
			tier:     TierOwn,
			features: []string{"sandbox"},
			probe:    prepareGVisorProbe(),
		},
		{
			name:     "explicit-none-refused",
			tier:     TierOwn,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:    prepareGVisorProbe(),
			opts:     HostPrepareOptionsV1{Level: "none"},
		},
		{
			name:     "shared-explicit-process-refused",
			tier:     TierShared,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe: api.CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.8.0", UserNS: true, LandlockABI: 3, Seccomp: true,
				Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true}},
			},
			opts: HostPrepareOptionsV1{Level: "process"},
		},
		{
			name:        "root-print-no-runnable-wall",
			tier:        TierOwn,
			features:    []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe:       api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.8.0", KVM: api.KVM{Present: true, Usable: false}},
			opts:        HostPrepareOptionsV1{RootPrinted: true},
			expectNoWal: true,
		},
		{
			name:     "shared-process-only-no-wall",
			tier:     TierShared,
			features: []string{"sandbox", api.FeatureSandboxPrepareV1},
			probe: api.CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.8.0", UserNS: true, LandlockABI: 3, Seccomp: true,
				Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true}},
			},
			expectNoWal: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Name: "box", SSH: "box.example", WallFloor: "gvisor", Prepared: basePrepared}
			target, _ := prepareSavedFixture(t, h, tc.tier, hostID, false)
			var calls atomic.Int32
			tr := newPrepareBridgeTransport(t, hostID, tc.features, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
				if req.Op == "sandbox.probe" {
					return tc.probe, nil, false
				}
				if req.Op == "sandbox.prepare" {
					calls.Add(1)
					if tc.reply != nil {
						return tc.reply(req)
					}
				}
				return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
			})
			conn, err := target.dialRoute(t.Context(), route{t: tr})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			req := NewHostPrepareGrantRequest()
			defer req.Close()
			ctx := WithGrantRequest(t.Context(), req)
			outcome, err := conn.PrepareSandboxV1(ctx, target, tc.opts)
			if tc.expectNoWal {
				if err != nil || outcome == nil || !outcome.NoRunnableWall() || outcome.Tested() || outcome.ObservedLevel() != "none" || calls.Load() != 0 {
					t.Fatalf("expected honest no-wall outcome with 0 prepare calls, got outcome=%+v err=%v calls=%d", outcome, err, calls.Load())
				}
				if _, recErr := RecordSandboxPreparationV1(ctx, target, outcome); recErr == nil {
					t.Fatal("RecordSandboxPreparationV1 accepted a no-wall outcome")
				}
			} else if err == nil || outcome != nil {
				t.Fatalf("expected failure for %s, got outcome=%+v err=%v", tc.name, outcome, err)
			}
			reloaded, ferr := FindHost(h.Name)
			if ferr != nil || reloaded.WallFloor != "gvisor" || reloaded.Prepared == nil ||
				reloaded.Prepared.Level != "gvisor" || reloaded.Prepared.MemTotal != 4<<30 ||
				!reloaded.Prepared.PreparedAt.Equal(basePrepared.PreparedAt) {
				t.Fatalf("existing floor/cache changed on %s: %+v %+v %v", tc.name, reloaded.WallFloor, reloaded.Prepared, ferr)
			}
		})
	}
}

func TestSandboxPrepareCancellationConfigTierAndKeyMutationFailClosed(t *testing.T) {
	hostID := strings.Repeat("e", 32)

	t.Run("inflight-cancel-and-close-join", func(t *testing.T) {
		h := Host{Name: "box", SSH: "box.example"}
		target, _ := prepareSavedFixture(t, h, TierOwn, hostID, false)
		entered := make(chan struct{})
		release := make(chan struct{})
		tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
			if req.Op == "sandbox.probe" {
				return prepareGVisorProbe(), nil, false
			}
			if req.Op == "sandbox.prepare" {
				close(entered)
				<-release
				return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 10}, nil, false
			}
			return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
		})
		conn, err := target.dialRoute(t.Context(), route{t: tr})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { close(release); conn.Close() }()
		req := NewHostPrepareGrantRequest()
		ctx := WithGrantRequest(t.Context(), req)
		done := make(chan error, 1)
		go func() {
			_, err := conn.PrepareSandboxV1(ctx, target, HostPrepareOptionsV1{})
			done <- err
		}()
		<-entered
		// Close while busy cancels active work and refuses releasing settings until joined.
		if err := req.Close(); err == nil {
			t.Fatal("GrantRequest.Close released settings while sandbox.prepare was in flight")
		}
		if err := <-done; err == nil {
			t.Fatal("canceled in-flight sandbox.prepare succeeded")
		}
		if err := req.Close(); err != nil {
			t.Fatalf("GrantRequest.Close after join failed: %v", err)
		}
		reloaded, err := FindHost(h.Name)
		if err != nil || reloaded.WallFloor != "" || reloaded.Prepared != nil {
			t.Fatalf("canceled boot wrote floor or cache: %+v", reloaded)
		}
	})

	for _, mutation := range []string{"config-before-record", "config-at-beforeSave", "tier-downgrade", "pin-deleted", "cancel-at-beforeSave"} {
		t.Run(mutation, func(t *testing.T) {
			h := Host{Name: "box", SSH: "box.example"}
			target, _ := prepareSavedFixture(t, h, TierOwn, hostID, true)
			if err := os.WriteFile(settingsPath(), []byte("[ui]\nmotion = \"auto\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
				if req.Op == "sandbox.probe" {
					return prepareGVisorProbe(), nil, false
				}
				if req.Op == "sandbox.prepare" {
					return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 15}, nil, false
				}
				return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
			})
			conn, err := target.dialRoute(t.Context(), route{t: tr})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			req := NewHostPrepareGrantRequest()
			defer req.Close()
			recCtx, cancelRec := context.WithCancel(t.Context())
			defer cancelRec()
			prepCtx := WithGrantRequest(recCtx, req)
			outcome, err := conn.PrepareSandboxV1(prepCtx, target, HostPrepareOptionsV1{})
			if err != nil {
				t.Fatal(err)
			}

			prevBefore := beforeHostsSave
			defer func() { beforeHostsSave = prevBefore }()
			switch mutation {
			case "config-before-record":
				if err := os.WriteFile(settingsPath(), []byte("[ui]\nmotion = \"off\"\n# mutated-before-record\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "config-at-beforeSave":
				beforeHostsSave = func() {
					_ = os.WriteFile(settingsPath(), []byte("[ui]\nmotion = \"off\"\n# mutated-at-beforeSave\n"), 0600)
				}
			case "tier-downgrade":
				if err := SetTierContext(t.Context(), h.Name, TierShared, hostID, ""); err != nil {
					t.Fatal(err)
				}
			case "pin-deleted":
				if err := os.Remove(pinPath(h.Name)); err != nil {
					t.Fatal(err)
				}
			case "cancel-at-beforeSave":
				beforeHostsSave = cancelRec
			}

			if _, err := RecordSandboxPreparationV1(prepCtx, target, outcome); err == nil {
				t.Fatalf("mutation %s did not block floor publication", mutation)
			}
			reloaded, err := FindHost(h.Name)
			if err != nil || reloaded.WallFloor != "" || reloaded.Prepared != nil {
				t.Fatalf("mutation %s leaked floor/cache write: floor=%q prepared=%+v err=%v", mutation, reloaded.WallFloor, reloaded.Prepared, err)
			}
		})
	}
}

func TestSandboxPrepareStrongerFloorPreservedAndWeakerFloorRaised(t *testing.T) {
	hostID := strings.Repeat("f", 32)
	cases := []struct {
		name          string
		tier          Tier
		initialFloor  string
		opts          HostPrepareOptionsV1
		wantFloor     string
		wantCachedLvl string
	}{
		{"stronger-microvm-preserved-on-gvisor-test", TierOwn, "microvm", HostPrepareOptionsV1{}, "microvm", "gvisor"},
		{"stronger-gvisor-preserved-on-explicit-process", TierOwn, "gvisor", HostPrepareOptionsV1{Level: "process"}, "gvisor", "process"},
		{"weaker-process-raised-to-gvisor", TierOwn, "process", HostPrepareOptionsV1{}, "gvisor", "gvisor"},
		{"absent-floor-established-on-shared", TierShared, "", HostPrepareOptionsV1{}, "gvisor", "gvisor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Name: "box", SSH: "box.example", WallFloor: tc.initialFloor}
			target, _ := prepareSavedFixture(t, h, tc.tier, hostID, false)
			tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
				if req.Op == "sandbox.probe" {
					return prepareGVisorProbe(), nil, false
				}
				if req.Op == "sandbox.prepare" {
					var args api.SandboxPrepareArgsV1
					if err := json.Unmarshal(req.Args, &args); err != nil {
						return nil, &proto.Error{Code: "bad_args", Message: err.Error()}, false
					}
					return api.SandboxPrepareResultV1{Version: 1, Level: args.Level, Driver: args.Level, BootMS: 21}, nil, false
				}
				return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
			})
			conn, err := target.dialRoute(t.Context(), route{t: tr})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			req := NewHostPrepareGrantRequest()
			defer req.Close()
			ctx := WithGrantRequest(t.Context(), req)
			outcome, err := conn.PrepareSandboxV1(ctx, target, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := RecordSandboxPreparationV1(ctx, target, outcome)
			if err != nil {
				t.Fatal(err)
			}
			if saved.WallFloor != tc.wantFloor {
				t.Fatalf("WallFloor = %q, want %q", saved.WallFloor, tc.wantFloor)
			}
			if saved.Prepared == nil || saved.Prepared.Level != tc.wantCachedLvl {
				t.Fatalf("Prepared = %+v, want Level %q", saved.Prepared, tc.wantCachedLvl)
			}
			reloaded, err := FindHost(h.Name)
			if err != nil || !SameEntry(reloaded, saved) {
				t.Fatalf("reloaded host differs from returned host: %v", err)
			}
		})
	}
}
