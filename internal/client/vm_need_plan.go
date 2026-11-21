package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// VMNeedOptionsV1 contains dimensions already resolved by the canonical policy
// and admission owner. This builder invents no defaults or lower memory cap.
type VMNeedOptionsV1 struct {
	Arch, Image, Intent string
	// Driver preserves an explicit existing selection when waking a sandbox.
	Driver   string
	GuestRAM int64
	CPUs     int
	DiskMax  int64
	Progress io.Writer
}

// VMNeedPlanV1 is an opaque locally built same-host publication plan. Only the
// actual builder fills it from signed runtime, client image lock and the
// canonical authenticated outside-G11 agents producer. A raw ContentRef or
// validated Need alone cannot construct this client provenance.
type VMNeedPlanV1 struct {
	need           vmneed.NeedV1
	hostID         string
	raw            json.RawMessage
	agents         image.AgentDiskV1
	conn           *Conn
	imageSelection *sandboxImageSelectionV1
}

// PrepareVMNeedV1 runs BEFORE secret values. Delivery and pin rules stay with
// their existing owners; the constructor never selects bytes from host runtime
// inventory, boot evidence or a mutable guest layout.
func (c *Conn) PrepareVMNeedV1(ctx context.Context, options VMNeedOptionsV1) (*VMNeedPlanV1, error) {
	return c.prepareVMNeedV1(ctx, options, nil)
}

// Only PrepareNewVM passes its already checked original request. The public
// builder cannot create a maintenance request or borrow a New operation.
func (c *Conn) prepareVMNeedV1(ctx context.Context, options VMNeedOptionsV1, original *GrantRequest) (_ *VMNeedPlanV1, returned error) {
	if original != nil {
		if original != grantRequestOf(ctx) {
			return nil, grantOperationError()
		}
		if err := requireGrantOperation(ctx, grantOpNew); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, _, closePolicy, err := withRetainedImagePolicy(ctx, c)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, closePolicy()) }()
	if !c.HasFeature(api.FeatureVMNeedV1) || !c.HasFeature(api.FeatureAgentsDiskV1) {
		return nil, c.grantHelperOld("prepare the authenticated microvm Need and agents disk")
	}
	if _, err := vmneed.BudgetFor(options.GuestRAM); err != nil {
		return nil, &UserError{Msg: CleanLine(err.Error()), Hint: "Choose an admitted positive microvm guest size.", Code: "vm_expected", Kind: KindFailed}
	}
	identity := c.HostID()
	if identity == "" {
		return nil, grantIdentityError(c.Host)
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return nil, err
	}
	if options.Arch != "" && options.Arch != probe.Arch {
		return nil, vmPrepareUnavailable()
	}
	arch := probe.Arch
	if probe.OS != "linux" {
		return nil, vmPrepareUnavailable()
	}
	runtime, err := c.prepareVMRuntimeV1(ctx, probe, options.Progress, original)
	if err != nil {
		return nil, err
	}
	probe, err = c.CapProbe(ctx, "")
	if err != nil {
		return nil, err
	}
	driver, err := selectVMDriverV1(probe, options.Driver, options.Intent)
	if err != nil {
		return nil, err
	}
	if err := vmneed.ValidateLimitsForDriver(driver, options.CPUs, options.DiskMax, arch); err != nil {
		return nil, &UserError{Msg: CleanLine(err.Error()), Hint: "Choose supported CPU and disk limits for this microvm.", Code: "vm_expected", Kind: KindFailed}
	}
	runtime, err = vmneed.SelectRuntimeForDriver(driver, runtime, arch)
	if err != nil {
		return nil, err
	}
	// Delivery above retains the complete original signed runtime request.
	// Only this Need selects the closed CHV boot roles; no signed bytes or
	// generic full-store record are replaced by the derived metadata.
	runtime, err = vmneed.SelectRuntimeForDriver("chv", runtime, arch)
	if err != nil {
		return nil, &UserError{Msg: "the original runtime has no valid selected VM boot set: " + CleanLine(err.Error()), Hint: "Install a current client release with the required VM, kernel and tools roles.", Code: "vm_expected", Kind: KindRefused}
	}
	if _, err := EnsureImage(ctx, c, options.Image, ImageOptions{Arch: arch, Progress: options.Progress}); err != nil {
		return nil, err
	}
	// Re-read the CLIENT lock after first-conversion recording. Do not promote
	// the preceding host response's root hash directly into execution authority.
	imageSelection, err := c.imageSelectionForNeedV1(ctx, options.Image, arch)
	if err != nil {
		return nil, err
	}
	agents, err := c.EnsureAgentsDiskV1(ctx, arch)
	if err != nil {
		return nil, err
	}
	need := vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: options.GuestRAM, Runtime: runtime, Image: imageSelection.Image.Clone(), Agents: agents.Content}, Driver: driver, CPUs: options.CPUs, DiskMax: options.DiskMax}
	if err := c.CheckVMFirstPrepareV1(ctx, need, options.Intent); err != nil {
		return nil, err
	}
	raw, err := vmneed.EncodeV1(need, arch)
	if err != nil {
		return nil, err
	}
	if c.HostID() != identity {
		return nil, grantIdentityError(c.Host)
	}
	return &VMNeedPlanV1{need: need.Clone(), hostID: identity, raw: append(json.RawMessage(nil), raw...), agents: agents, conn: c, imageSelection: cloneSandboxImageSelection(imageSelection)}, nil
}

// Need returns an independent typed copy, never mutable host inventory.
func (p *VMNeedPlanV1) Need() vmneed.NeedV1 {
	if p == nil {
		return vmneed.NeedV1{}
	}
	return p.need.Clone()
}

// EncodedFor returns the exact bytes H5 must bind and compare under beginGrant
// on this SAME authenticated request/connection. It confers no bypass itself.
func (p *VMNeedPlanV1) EncodedFor(c *Conn) (json.RawMessage, error) {
	if p == nil || c == nil || p.conn != c || p.hostID == "" || c.HostID() != p.hostID || len(p.raw) == 0 {
		return nil, vmPrepareUnavailable()
	}
	return append(json.RawMessage(nil), p.raw...), nil
}

// Actual New retains its locally computed signed request across the upgrade.
// Installed bytes only avoid an unnecessary outbound consent question; the
// original delivery receipt and first physical construction checks still apply.
func (c *Conn) prepareVMRuntimeV1(ctx context.Context, probe api.CapProbe, progress io.Writer, original *GrantRequest) (supply.Manifest, error) {
	if original == nil {
		if _, err := EnsureRuntime(ctx, c, RuntimeOptions{Probe: &probe, Progress: progress}); err != nil {
			return supply.Manifest{}, err
		}
		return runtimeManifest(probe.Arch)
	}
	if original != grantRequestOf(ctx) {
		return supply.Manifest{}, grantOperationError()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return supply.Manifest{}, err
	}
	request, err := runtimeRequestFor(probe.Arch)
	if err != nil {
		return supply.Manifest{}, err
	}
	if _, err := UpgradeRuntimeOnUse(ctx, c, RuntimeUpgradeOptions{RuntimeOptions: RuntimeOptions{Probe: &probe, Progress: progress}}); err != nil {
		return supply.Manifest{}, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return supply.Manifest{}, err
	}
	if _, err := runtimeReady(ctx, c, request); err != nil {
		return supply.Manifest{}, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return supply.Manifest{}, err
	}
	return cloneNewRuntimeManifest(request.Manifest), nil
}
