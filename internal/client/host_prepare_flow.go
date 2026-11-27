package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

type hostPrepareTestRouteKey struct{}

// PrepareGrantFuncV1 runs a gated phase closure, prompting for any live
// terminal ConfirmError on that phase's own GrantRequest and returning the
// updated phase context. Answers stay scoped to that single GrantRequest.
type PrepareGrantFuncV1 func(ctx context.Context, run func(context.Context) error) (context.Context, error)

func runPrepareGrantV1(ctx context.Context, ask PrepareGrantFuncV1, run func(context.Context) error) (context.Context, error) {
	if ask == nil {
		return ctx, run(ctx)
	}
	return ask(ctx, run)
}

func dialPrepareTargetV1(ctx context.Context, target *HostPrepareTargetV1, wake, interactive bool) (*Conn, error) {
	if target == nil {
		return nil, sandboxPrepareError("sandbox preparation needs its original saved target")
	}
	dialCtx := ctx
	if !wake {
		dialCtx = WithoutWake(ctx)
	}
	if rt, ok := dialCtx.Value(hostPrepareTestRouteKey{}).(route); ok && rt.t != nil {
		return target.dialRoute(dialCtx, rt)
	}
	return target.Dial(dialCtx, interactive)
}

// ResolveHostPrepareFetchModeV1 validates an explicit --images value or reads
// the configured [images] fetch preference when explicit is empty.
func ResolveHostPrepareFetchModeV1(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return RuntimeFetchMode(explicit)
	}
	return RuntimeFetchPreference(ctx)
}

func withPrepareImagesFetchV1(ctx context.Context, explicit string) (context.Context, string, error) {
	mode, err := ResolveHostPrepareFetchModeV1(ctx, explicit)
	if err != nil {
		return nil, "", err
	}
	if explicit != "" {
		ctx = context.WithValue(ctx, runtimeFetchKey{}, mode)
	}
	return ctx, mode, nil
}

func prepareReleaseProbeURLV1(effectiveMode string) (string, error) {
	if effectiveMode == "laptop" {
		return "", nil
	}
	hasRuntime, err := HasReleaseRuntime()
	if err != nil || !hasRuntime {
		return "", err
	}
	m, err := ReleaseManifest()
	if err != nil {
		return "", err
	}
	return supply.ReleaseFolder(m.Release), nil
}

// ProbeHostPrepareV1 runs Phase 1 of sandbox preparation on its own saved-host
// connection bound to target, then closes that connection before returning.
func ProbeHostPrepareV1(ctx context.Context, target *HostPrepareTargetV1, explicitImages string, wake, interactive bool) (api.CapProbe, string, error) {
	if target == nil {
		return api.CapProbe{}, "", sandboxPrepareError("sandbox preparation needs its original saved target")
	}
	pctx, mode, err := withPrepareImagesFetchV1(ctx, explicitImages)
	if err != nil {
		return api.CapProbe{}, "", err
	}
	releaseURL, err := prepareReleaseProbeURLV1(mode)
	if err != nil {
		return api.CapProbe{}, "", err
	}
	conn, err := dialPrepareTargetV1(pctx, target, wake, interactive)
	if err != nil {
		return api.CapProbe{}, "", err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(pctx, releaseURL)
	if err != nil {
		return api.CapProbe{}, "", err
	}
	if err := target.checkConn(pctx, conn); err != nil {
		return api.CapProbe{}, "", err
	}
	return probe, mode, nil
}

// DeliverHostPrepareRuntimeV1 runs Phase 2 of sandbox preparation under its own
// closed NewRuntimeUpgradeGrantRequest and dedicated saved-host connection.
func DeliverHostPrepareRuntimeV1(ctx context.Context, target *HostPrepareTargetV1, explicitImages string, wake bool, progress io.Writer, askGrant PrepareGrantFuncV1) (status api.RuntimeStatus, returned error) {
	if target == nil {
		return status, sandboxPrepareError("runtime preparation needs its original saved target")
	}
	rctx, mode, err := withPrepareImagesFetchV1(ctx, explicitImages)
	if err != nil {
		return status, err
	}
	if err := target.Check(rctx); err != nil {
		return status, err
	}
	hasRuntime, err := HasReleaseRuntime()
	if err != nil || !hasRuntime {
		return status, err
	}
	releaseURL, err := prepareReleaseProbeURLV1(mode)
	if err != nil {
		return status, err
	}
	req := NewRuntimeUpgradeGrantRequest()
	bounded, cancel := context.WithTimeout(rctx, 5*time.Minute)
	defer cancel()
	phaseCtx := WithGrantRequest(bounded, req)
	defer func() { returned = errors.Join(returned, req.Close()) }()

	conn, err := dialPrepareTargetV1(phaseCtx, target, wake, false)
	if err != nil {
		return status, err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(phaseCtx, releaseURL)
	if err != nil {
		return status, err
	}
	if err := target.checkConn(phaseCtx, conn); err != nil {
		return status, err
	}
	opts := RuntimeUpgradeOptions{RuntimeOptions: RuntimeOptions{Probe: &probe, Progress: progress}}
	if explicitImages != "" {
		opts.Mode = mode
	}
	_, err = runPrepareGrantV1(phaseCtx, askGrant, func(gctx context.Context) error {
		var uerr error
		status, uerr = UpgradeRuntimeOnUse(gctx, conn, opts)
		return uerr
	})
	return status, err
}

// BootAndRecordHostPrepareV1 runs Phase 4 (fixed sandbox test boot) under its
// own NewHostPrepareGrantRequest and dedicated saved-host connection, and
// publishes WallFloor + Prepared ONLY when outcome.Tested() succeeds.
func BootAndRecordHostPrepareV1(ctx context.Context, target *HostPrepareTargetV1, explicitImages string, opts HostPrepareOptionsV1, wake, interactive bool, askGrant PrepareGrantFuncV1) (outcome *SandboxPrepareOutcomeV1, saved Host, returned error) {
	if target == nil {
		return nil, Host{}, sandboxPrepareError("sandbox preparation needs its original saved target")
	}
	bctx, _, err := withPrepareImagesFetchV1(ctx, explicitImages)
	if err != nil {
		return nil, Host{}, err
	}
	req := NewHostPrepareGrantRequest()
	phaseCtx := WithGrantRequest(bctx, req)
	defer func() { returned = errors.Join(returned, req.Close()) }()

	conn, err := dialPrepareTargetV1(phaseCtx, target, wake, interactive)
	if err != nil {
		return nil, Host{}, err
	}
	defer conn.Close()

	grantCtx, err := runPrepareGrantV1(phaseCtx, askGrant, func(gctx context.Context) error {
		var perr error
		outcome, perr = conn.PrepareSandboxV1(gctx, target, opts)
		return perr
	})
	if err != nil {
		return nil, Host{}, err
	}
	if outcome == nil {
		return nil, Host{}, sandboxPrepareProtocolError("the fixed sandbox preparation returned no outcome")
	}
	if outcome != nil && outcome.Tested() {
		saved, err = RecordSandboxPreparationV1(grantCtx, target, outcome)
		if err != nil {
			return outcome, Host{}, err
		}
		return outcome, saved, nil
	}
	return outcome, target.Host(), nil
}

// FormatSandboxProbeLinesV1 formats cleaned driver probe lines for terminal
// and wizard display.
func FormatSandboxProbeLinesV1(probe api.CapProbe) []string {
	clean := CleanCapProbe(probe)
	nowLevel := BestWallLevel(clean).String()
	_, afterRoot := api.LevelsFor(clean, api.RuntimeStatus{})
	var lines []string
	if n := clean.Nitro; n != nil && n.Present {
		lines = append(lines, fmt.Sprintf("Nitro device: %s, parent %d vCPUs, budget %d, slots %d (%d in use), pool %d bytes free", n.Mode, n.Pools["vcpus"], n.Pools["vcpu_budget"], n.Pools["pcie_slots"], n.Pools["pcie_slots_in_use"], n.Pools["mem_pool_free"]))
	}
	for _, w := range clean.Walls {
		mark, detail := "✕", w.Reason
		if w.OK {
			mark, detail = "✓", w.Level
			if w.Version != "" {
				detail += " (" + w.Version + ")"
			}
		} else if detail == "" {
			detail = "unavailable"
		}
		lines = append(lines, fmt.Sprintf("%s %s: %s", mark, w.Driver, detail))
	}
	summary := "Available wall now: " + CleanLine(nowLevel)
	if afterRoot != "" && afterRoot != "none" && afterRoot != nowLevel {
		summary += " (" + CleanLine(afterRoot) + " after optional root steps)"
	}
	return append(lines, strings.TrimSpace(summary))
}
