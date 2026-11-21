package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func saveTestHostWithTier(t *testing.T, h client.Host, tier client.Tier) *client.HostPrepareTargetV1 {
	t.Helper()
	newTestHome(t)
	ctx := context.Background()
	if err := client.SaveHostsContext(ctx, []client.Host{h}); err != nil {
		t.Fatal(err)
	}
	hostID := strings.Repeat("a", 32)
	if err := client.SetTierContext(ctx, h.Name, tier, hostID, ""); err != nil {
		t.Fatal(err)
	}
	target, err := client.OpenHostPrepareTargetV1(ctx, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func stubHostPrepareHooks(t *testing.T) {
	t.Helper()
	origOpen := openHostPrepareTarget
	origProbe := probeHostPrepare
	origRuntime := deliverHostPrepareRuntime
	origRoot := runHostPrepareRoot
	origBoot := bootHostPrepare
	origStd := connectHostStandard
	origPrep := connectHostPrepare
	t.Cleanup(func() {
		openHostPrepareTarget = origOpen
		probeHostPrepare = origProbe
		deliverHostPrepareRuntime = origRuntime
		runHostPrepareRoot = origRoot
		bootHostPrepare = origBoot
		connectHostStandard = origStd
		connectHostPrepare = origPrep
	})
}

func commitRegisteredTestHost(t *testing.T, ctx context.Context, save client.HostChange, chooseTier func() (client.Tier, error), log *onboard.Log) *client.HostPrepareTargetV1 {
	t.Helper()
	if chooseTier != nil {
		tier, err := chooseTier()
		if err != nil {
			t.Fatal(err)
		}
		save.Tier = tier
	}
	save.HostID = strings.Repeat("b", 32)
	// This hook replaces connectHostCore, so retain its real owned stage.
	// An existing native pin models an already-pinned host without SSH.
	original := ctx
	ctx, abort, err := client.StagePinOwned(ctx, save.Now.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	want := pinFor(t, save.Now.Name)
	save.Pin = true
	obs, err := client.ObserveHostRegistrationContext(ctx, save)
	if err != nil {
		t.Fatal(err)
	}
	target, err := client.UpdateHostsForPreparation(client.WithHostStateObservation(ctx, obs), save)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := client.PinOf(save.Now.Name)
	if err != nil || pin.Fingerprint != want {
		t.Fatalf("registration did not preserve its actual native pin: pin=%+v err=%v", pin, err)
	}
	if err := target.Check(original); err != nil {
		t.Fatalf("committed target lost its original saved identity: %v", err)
	}
	log.Add("saved the host %s", save.Now.Name)
	return target
}

func TestHostPrepareFlagValidationAndMissingHost(t *testing.T) {
	newTestHome(t)

	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"host", "prepare", "box", "--root-step", "later"}, 2, `"later" is not a root step mode`},
		{[]string{"host", "prepare", "box", "--images", "usb"}, 2, `"usb" is not an images fetch mode`},
		{[]string{"host", "prepare", "box", "--images", ""}, 2, `"" is not an images fetch mode`},
		{[]string{"host", "prepare", "missing"}, 3, `no host named "missing"`},
		{[]string{"host", "add", "box", "--no-sandbox", "--images", "laptop"}, 2, "--no-sandbox cannot be combined with --images"},
		{[]string{"host", "add", "box", "--no-sandbox", "--root-step", "skip"}, 2, "--no-sandbox cannot be combined with --root-step"},
		{[]string{"host", "add", "box", "--tier", "own", "--yes", "--images", "usb"}, 2, `"usb" is not an images fetch mode`},
		{[]string{"host", "add", "box", "--tier", "own", "--yes", "--root-step", "later"}, 2, `"later" is not a root step mode`},
	} {
		_, stderr, exit := mainLine(t, tc.args...)
		if exit != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit=%d, want %d containing %q:\n%s", tc.args, exit, tc.code, tc.want, stderr)
		}
	}
}

func TestHostPrepareOrdersFourPhasesOnOriginalTargetAndPrintsHonestState(t *testing.T) {
	saveTestHostWithTier(t, client.Host{Name: "box", SSH: "box.example", WallFloor: "microvm"}, client.TierOwn)
	stubHostPrepareHooks(t)

	var order []string
	var opened *client.HostPrepareTargetV1
	openHostPrepareTarget = func(ctx context.Context, name string) (*client.HostPrepareTargetV1, error) {
		order = append(order, "open")
		target, err := client.OpenHostPrepareTargetV1(ctx, name)
		opened = target
		return target, err
	}
	probeHostPrepare = func(_ context.Context, target *client.HostPrepareTargetV1, images string, _, _ bool) (api.CapProbe, string, error) {
		if target != opened || images != "laptop" {
			t.Fatalf("probe got wrong target or images=%q", images)
		}
		order = append(order, "probe")
		return api.CapProbe{
			OS: "linux", Arch: "amd64", User: "dev", UID: 1000,
			Walls: []api.WallProbe{{Driver: "gvisor", Level: "gvisor", OK: true}},
		}, "laptop", nil
	}
	deliverHostPrepareRuntime = func(_ context.Context, target *client.HostPrepareTargetV1, images string, _ bool, _ io.Writer, _ client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
		if target != opened || images != "laptop" {
			t.Fatalf("runtime got wrong target or images=%q", images)
		}
		order = append(order, "runtime")
		return api.RuntimeStatus{}, nil
	}
	runHostPrepareRoot = func(_ context.Context, target *client.HostPrepareTargetV1, opts client.HostPrepareRootOptionsV1) (client.HostPrepareRootOutcomeV1, error) {
		if target != opened || opts.ExplicitMode != client.RootSkip || !opts.DelegateIO || !opts.SeparateAccounts {
			t.Fatalf("root got unexpected target or opts: %+v", opts)
		}
		order = append(order, "root")
		return client.HostPrepareRootOutcomeV1{Needed: true, Mode: client.RootSkip, Skipped: true}, nil
	}
	bootHostPrepare = func(_ context.Context, target *client.HostPrepareTargetV1, images string, opts client.HostPrepareOptionsV1, _, _ bool, _ client.PrepareGrantFuncV1) (prepareOutcomeView, client.Host, error) {
		if target != opened || images != "laptop" || opts.RootPrinted {
			t.Fatalf("boot got unexpected args: images=%q rootPrinted=%v", images, opts.RootPrinted)
		}
		order = append(order, "boot")
		return prepareOutcomeView{
			tested:         true,
			levelAfterRoot: "microvm",
			result:         api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 21},
		}, client.Host{Name: "box", SSH: "box.example", WallFloor: "microvm"}, nil
	}

	out, stderr, code := mainLine(t, "host", "prepare", "box", "--images", "laptop", "--root-step", "skip", "--delegate-io", "--separate-accounts")
	if code != 0 || out != "" {
		t.Fatalf("exit=%d out=%q stderr=\n%s", code, out, stderr)
	}
	if got := strings.Join(order, "->"); got != "open->probe->runtime->root->boot" {
		t.Fatalf("phase order = %q, want open->probe->runtime->root->boot", got)
	}
	if !strings.Contains(stderr, "Sandbox wall gvisor (gvisor) tested in 21 ms on box. Existing stronger floor microvm kept.") {
		t.Errorf("missing preserved stronger floor line:\n%s", stderr)
	}
}

func TestHostPreparePrintedNoWallAndNoInputRootConsent(t *testing.T) {
	t.Run("printed_root_with_no_runnable_wall_prints_honest_state", func(t *testing.T) {
		saveTestHostWithTier(t, client.Host{Name: "box", SSH: "box.example"}, client.TierOwn)
		stubHostPrepareHooks(t)

		probeHostPrepare = func(_ context.Context, _ *client.HostPrepareTargetV1, _ string, _, _ bool) (api.CapProbe, string, error) {
			return api.CapProbe{
				OS: "linux", Arch: "amd64", User: "dev", UID: 1000,
				Walls: []api.WallProbe{{Driver: "microvm", Level: "microvm", OK: false, Reason: "/dev/kvm denied"}},
			}, "auto", nil
		}
		deliverHostPrepareRuntime = func(context.Context, *client.HostPrepareTargetV1, string, bool, io.Writer, client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
			return api.RuntimeStatus{}, nil
		}
		runHostPrepareRoot = func(_ context.Context, _ *client.HostPrepareTargetV1, _ client.HostPrepareRootOptionsV1) (client.HostPrepareRootOutcomeV1, error) {
			return client.HostPrepareRootOutcomeV1{
				Needed: true, Mode: client.RootPrint, Printed: true,
				ScriptFile: "./" + brand.Name + "-root-box.sh", SHA256: strings.Repeat("f", 64),
			}, nil
		}
		bootHostPrepare = func(_ context.Context, target *client.HostPrepareTargetV1, _ string, opts client.HostPrepareOptionsV1, _, _ bool, _ client.PrepareGrantFuncV1) (prepareOutcomeView, client.Host, error) {
			if !opts.RootPrinted {
				t.Fatal("expected RootPrinted=true forwarded to Phase 4")
			}
			return prepareOutcomeView{
				tested: false, noRunnableWall: true, levelAfterRoot: "microvm", rootPrinted: true,
			}, target.Host(), nil
		}

		_, stderr, code := mainLine(t, "host", "prepare", "box", "--root-step", "print")
		if code != 0 {
			t.Fatalf("exit=%d:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "No sandbox wall can run on box yet. Its wall floor was not changed.") ||
			!strings.Contains(stderr, "Run the printed root script on box to enable microvm") ||
			strings.Contains(stderr, "is ready") {
			t.Fatalf("dishonest no-wall output:\n%s", stderr)
		}
	})

	t.Run("root_run_never_consumes_non_interactive_or_donated_consent", func(t *testing.T) {
		var out bytes.Buffer
		p := testPrompts("", &out)
		p.on = false
		h := client.Host{Name: "box", SSH: "box.example"}
		probe := api.CapProbe{OS: "linux", Arch: "amd64", User: "dev", UID: 1000, Sudo: true, SudoPasswordless: false}

		err := authoriseRootRun(p, h, client.TierOwn, probe, true, false)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "root_step_terminal" || ue.Kind != client.KindNeedsAnswer {
			t.Fatalf("expected root_step_terminal KindNeedsAnswer with input off, got %v", err)
		}

		err = authoriseRootRun(p, h, client.TierOwn, probe, true, true)
		if !errors.As(err, &ue) || ue.Code != "root_step_password" || ue.Kind != client.KindFailed {
			t.Fatalf("expected root_step_password KindFailed when --yes lacks passwordless sudo, got %v", err)
		}
	})
}

func TestHostAddNoSandboxAndRegistrationCommitTargetFlows(t *testing.T) {
	t.Run("no_sandbox_keeps_prior_flow_without_running_prepare_phases", func(t *testing.T) {
		newTestHome(t)
		stubHostPrepareHooks(t)

		connectHostStandard = func(ctx context.Context, _ io.Writer, save client.HostChange, _ func() (client.Tier, error), _ func(string) (bool, error), log *onboard.Log, _ *client.PinOptions) ([]api.AgentCheck, error) {
			save.Pin = false
			save.HostID = strings.Repeat("c", 32)
			if err := client.UpdateHosts(ctx, save); err != nil {
				return nil, err
			}
			log.Add("saved the host %s", save.Now.Name)
			return nil, nil
		}
		probeHostPrepare = func(context.Context, *client.HostPrepareTargetV1, string, bool, bool) (api.CapProbe, string, error) {
			t.Fatal("--no-sandbox must not call probeHostPrepare")
			return api.CapProbe{}, "", nil
		}

		_, stderr, code := mainLine(t, "host", "add", "box", "dev@box.example", "--tier", "own", "--yes", "--no-sandbox")
		if code != 0 || !strings.Contains(stderr, "box is ready.") {
			t.Fatalf("exit=%d:\n%s", code, stderr)
		}
		saved, err := client.FindHost("box")
		if err != nil || saved.WallFloor != "" || saved.Prepared != nil {
			t.Fatalf("saved host = %+v, err = %v", saved, err)
		}
	})

	t.Run("default_host_add_uses_registration_commit_target_and_reports_honest_failure_or_cancel", func(t *testing.T) {
		newTestHome(t)
		stubHostPrepareHooks(t)

		var committed *client.HostPrepareTargetV1
		connectHostPrepare = func(ctx context.Context, _ io.Writer, save client.HostChange, chooseTier func() (client.Tier, error), _ func(string) (bool, error), log *onboard.Log, _ *client.PinOptions) ([]api.AgentCheck, *client.HostPrepareTargetV1, error) {
			committed = commitRegisteredTestHost(t, ctx, save, chooseTier, log)
			if !committed.FromRegistration() {
				t.Fatal("expected registration-committed HostPrepareTargetV1")
			}
			return nil, committed, nil
		}
		probeHostPrepare = func(_ context.Context, target *client.HostPrepareTargetV1, images string, _, _ bool) (api.CapProbe, string, error) {
			if target != committed || images != "host" {
				t.Fatalf("host add passed unexpected target or images=%q", images)
			}
			return api.CapProbe{OS: "linux", Arch: "amd64", User: "dev", UID: 1000}, "host", nil
		}
		deliverHostPrepareRuntime = func(_ context.Context, target *client.HostPrepareTargetV1, _ string, _ bool, _ io.Writer, _ client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
			if target != committed {
				t.Fatal("runtime phase did not receive committed registration target")
			}
			return api.RuntimeStatus{}, nil
		}
		runHostPrepareRoot = func(_ context.Context, target *client.HostPrepareTargetV1, _ client.HostPrepareRootOptionsV1) (client.HostPrepareRootOutcomeV1, error) {
			if target != committed {
				t.Fatal("root phase did not receive committed registration target")
			}
			return client.HostPrepareRootOutcomeV1{Needed: false}, nil
		}
		bootHostPrepare = func(_ context.Context, target *client.HostPrepareTargetV1, _ string, _ client.HostPrepareOptionsV1, _, _ bool, _ client.PrepareGrantFuncV1) (prepareOutcomeView, client.Host, error) {
			if target != committed {
				t.Fatal("boot phase did not receive committed registration target")
			}
			return prepareOutcomeView{}, client.Host{}, &client.UserError{
				Msg: "test boot failed", Hint: brand.Name + " host doctor box", Code: "sandbox_boot", Kind: client.KindFailed,
			}
		}

		_, stderr, code := mainLine(t, "host", "add", "box", "dev@box.example", "--tier", "own", "--yes", "--images", "host")
		if code != 1 || strings.Contains(stderr, "is ready") {
			t.Fatalf("failed test boot in host add returned exit=%d:\n%s", code, stderr)
		}
		saved, err := client.FindHost("box")
		if err != nil || saved.WallFloor != "" || saved.Prepared != nil {
			t.Fatalf("saved host after failed boot = %+v, err = %v", saved, err)
		}
	})

	t.Run("cancel_during_prepare_preserves_saved_host_change_log", func(t *testing.T) {
		newTestHome(t)
		stubHostPrepareHooks(t)

		connectHostPrepare = func(ctx context.Context, _ io.Writer, save client.HostChange, chooseTier func() (client.Tier, error), _ func(string) (bool, error), log *onboard.Log, _ *client.PinOptions) ([]api.AgentCheck, *client.HostPrepareTargetV1, error) {
			return nil, commitRegisteredTestHost(t, ctx, save, chooseTier, log), nil
		}
		probeHostPrepare = func(context.Context, *client.HostPrepareTargetV1, string, bool, bool) (api.CapProbe, string, error) {
			return api.CapProbe{OS: "linux", Arch: "amd64", User: "dev", UID: 1000}, "auto", nil
		}
		deliverHostPrepareRuntime = func(context.Context, *client.HostPrepareTargetV1, string, bool, io.Writer, client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
			return api.RuntimeStatus{}, nil
		}
		runHostPrepareRoot = func(context.Context, *client.HostPrepareTargetV1, client.HostPrepareRootOptionsV1) (client.HostPrepareRootOutcomeV1, error) {
			return client.HostPrepareRootOutcomeV1{}, cancelled()
		}

		_, stderr, code := mainLine(t, "host", "add", "box", "dev@box.example", "--tier", "own", "--yes")
		if code != 130 || !strings.Contains(stderr, "Done before that: saved the host box.") {
			t.Fatalf("cancel exit=%d:\n%s", code, stderr)
		}
	})
}
