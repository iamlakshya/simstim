package client

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

func validateRequestedPrepareLevel(target *HostPrepareTargetV1, requested string) error {
	if requested == "" {
		return nil
	}
	level, err := wall.ParseLevel(requested)
	if err != nil {
		return &UserError{
			Msg:  fmt.Sprintf("%q is not a wall level", CleanLine(requested)),
			Hint: "Use process, gvisor or microvm, or leave the level empty.",
			Code: "bad_floor",
			Kind: KindUsage,
		}
	}
	if level == wall.None {
		return &UserError{
			Msg:  "sandbox preparation cannot test the none wall level",
			Hint: "Choose process, gvisor or microvm, or leave the level empty to test the strongest permitted wall.",
			Code: "bad_floor",
			Kind: KindRefused,
		}
	}
	return allowSandboxPrepareFloorV1(target, level.String())
}

func selectSandboxPrepareLevelV1(target *HostPrepareTargetV1, c *Conn, probe api.CapProbe, requested string) (string, bool, error) {
	if requested != "" {
		level, err := wall.ParseLevel(requested)
		if err != nil {
			return "", false, err
		}
		if err := allowSandboxPrepareFloorV1(target, level.String()); err != nil {
			return "", false, err
		}
		if level == wall.MicroVM {
			if probe.OS != "linux" || !probeHasPreparableMicroVM(probe) {
				return "", false, vmPrepareUnavailable()
			}
			if !c.HasFeature(api.FeatureVMNeedV1) || !c.HasFeature(api.FeatureAgentsDiskV1) || !c.HasFeature(api.FeatureRuntimeOriginV1) {
				return "", false, c.grantHelperOld("prepare the authenticated microvm Need and agents disk")
			}
			return "microvm", true, nil
		}
		if !probeHasOKWall(probe, level.String()) {
			return "", false, floorAboveBest(target.host, level, BestWallLevel(probe))
		}
		return level.String(), true, nil
	}
	best := wall.None
	canVM := probe.OS == "linux" && c.HasFeature(api.FeatureVMNeedV1) && c.HasFeature(api.FeatureAgentsDiskV1)
	for _, w := range probe.Walls {
		level, err := wall.ParseLevel(w.Level)
		if err != nil || level <= wall.None || level <= best {
			continue
		}
		if allowSandboxPrepareFloorV1(target, level.String()) != nil {
			continue
		}
		if level == wall.MicroVM {
			if probe.OS == "linux" && (w.OK || w.CanPrepare) && (!c.HasFeature(api.FeatureVMNeedV1) || !c.HasFeature(api.FeatureAgentsDiskV1) || !c.HasFeature(api.FeatureRuntimeOriginV1)) {
				return "", false, c.grantHelperOld("prepare the original complete microvm runtime")
			}
			if canVM && (w.OK || w.CanPrepare) {
				best = level
			}
			continue
		}
		if w.OK {
			best = level
		}
	}
	if best == wall.None {
		return "", false, nil
	}
	return best.String(), true, nil
}

func probeHasOKWall(probe api.CapProbe, level string) bool {
	return slices.ContainsFunc(probe.Walls, func(w api.WallProbe) bool { return w.Level == level && w.OK })
}

func probeHasPreparableMicroVM(probe api.CapProbe) bool {
	return slices.ContainsFunc(probe.Walls, func(w api.WallProbe) bool { return w.Level == "microvm" && (w.OK || w.CanPrepare) })
}

func (c *Conn) prepareSandboxArtifactsV1(ctx context.Context, plan *sandboxPreparePlanV1, probe api.CapProbe, level string, progress io.Writer) (api.SandboxPrepareArgsV1, string, string, error) {
	args := api.SandboxPrepareArgsV1{Version: 1, Level: level}
	switch level {
	case "process":
		return args, "process", "", nil
	case "gvisor":
		manifest, err := c.prepareSandboxGVisorRuntimeV1(ctx, probe)
		if err != nil {
			return api.SandboxPrepareArgsV1{}, "", "", err
		}
		runtimeName := ""
		if manifest != nil {
			args.Runtime = manifest
			runtimeName = manifest.Runtime
		}
		return args, "gvisor", runtimeName, nil
	case "microvm":
		plan.mu.Lock()
		if plan.closing || plan.closed {
			plan.mu.Unlock()
			return api.SandboxPrepareArgsV1{}, "", "", grantOperationError()
		}
		plan.imagePhaseActive = true
		plan.mu.Unlock()
		vmPlan, err := c.PrepareVMNeedV1(ctx, VMNeedOptionsV1{
			Arch:     probe.Arch,
			Image:    "default",
			Intent:   "microvm",
			GuestRAM: api.SandboxPrepareGuestRAMV1,
			CPUs:     api.SandboxPrepareCPUsV1,
			DiskMax:  api.SandboxPrepareDiskV1,
			Progress: progress,
		})
		plan.mu.Lock()
		plan.imagePhaseActive = false
		plan.mu.Unlock()
		if err != nil {
			return api.SandboxPrepareArgsV1{}, "", "", err
		}
		rawNeed, err := vmPlan.EncodedFor(c)
		if err != nil {
			return api.SandboxPrepareArgsV1{}, "", "", err
		}
		need := vmPlan.Need()
		rt := cloneSandboxPrepareManifest(need.Runtime)
		plan.mu.Lock()
		plan.vm = vmPlan
		plan.mu.Unlock()
		args.Runtime = &rt
		args.RuntimeOrigin, err = vmPlan.runtimeOriginForV1(ctx, c)
		if err != nil {
			return api.SandboxPrepareArgsV1{}, "", "", err
		}
		args.VMNeedV1 = rawNeed
		return args, need.Driver, rt.Runtime, nil
	default:
		return api.SandboxPrepareArgsV1{}, "", "", grantOperationError()
	}
}

func (c *Conn) prepareSandboxGVisorRuntimeV1(ctx context.Context, probe api.CapProbe) (*supply.Manifest, error) {
	m, err := ReleaseManifest()
	if err != nil {
		return nil, err
	}
	hasRuntime := false
	for _, item := range m.Items {
		if runtimecache.IsRuntime(item.Kind) {
			hasRuntime = true
			break
		}
	}
	if !hasRuntime {
		return nil, nil
	}
	request, err := runtimeRequestFor(probe.Arch)
	if err != nil {
		return nil, err
	}
	if _, err := runtimeReady(ctx, c, request); err != nil {
		return nil, err
	}
	manifest := cloneSandboxPrepareManifest(request.Manifest)
	return &manifest, nil
}

func cloneSandboxPrepareManifest(m supply.Manifest) supply.Manifest {
	m.Items = slices.Clone(m.Items)
	m.Advisories = slices.Clone(m.Advisories)
	return m
}

func sandboxPrepareError(msg string) error {
	return &UserError{
		Msg:  msg,
		Hint: "Start a fresh host preparation phase on the saved host.",
		Code: "sandbox_prepare",
		Kind: KindRefused,
	}
}

func sandboxPrepareProtocolError(msg string) error {
	return &UserError{
		Msg:  msg,
		Hint: "Upgrade the host helper and run host prepare again.",
		Code: "sandbox_prepare_protocol",
		Kind: KindRefused,
	}
}
