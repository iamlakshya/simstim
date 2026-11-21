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
	need            vmneed.NeedV1
	originalRuntime supply.Manifest
	hostID          string
	raw             json.RawMessage
	agents          image.AgentDiskV1
	conn            *Conn
	stamp           *hostTransportStamp
	request         *GrantRequest
	imageSelection  *sandboxImageSelectionV1
}

// PrepareVMNeedV1 runs BEFORE secret values. Delivery and pin rules stay with
// their existing owners; the constructor never selects bytes from host runtime
// inventory, boot evidence or a mutable guest layout.
func (c *Conn) PrepareVMNeedV1(ctx context.Context, options VMNeedOptionsV1) (*VMNeedPlanV1, error) {
	return c.prepareVMNeedV1(ctx, options, nil)
}

// Actual New and the retained cold Resume pass their checked original request.
// The public builder cannot borrow either original operation.
func (c *Conn) prepareVMNeedV1(ctx context.Context, options VMNeedOptionsV1, original *GrantRequest) (_ *VMNeedPlanV1, returned error) {
	if original != nil {
		if original != grantRequestOf(ctx) {
			return nil, grantOperationError()
		}
		if err := requireVMOriginalRequestV1(ctx, c); err != nil {
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
	if !c.HasFeature(api.FeatureVMNeedV1) || !c.HasFeature(api.FeatureAgentsDiskV1) || !c.HasFeature(api.FeatureRuntimeOriginV1) {
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
	originalRuntime := cloneNewRuntimeManifest(runtime)
	// Delivery above retains the complete original signed runtime request.
	// Select the chosen driver's closed boot roles once. This Need projection
	// never replaces the signed delivery document or its original receipt.
	runtime, err = vmneed.SelectRuntimeForDriver(driver, runtime, arch)
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
	return &VMNeedPlanV1{need: need.Clone(), originalRuntime: originalRuntime, hostID: identity, raw: append(json.RawMessage(nil), raw...), agents: agents, conn: c, stamp: c.hostTransport, request: grantRequestOf(ctx), imageSelection: cloneSandboxImageSelection(imageSelection)}, nil
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

// Actual New and cold Resume retain their locally computed signed request.
// Installed bytes only avoid an unnecessary outbound consent question; the
// original delivery receipt and first physical construction checks still apply.
func (c *Conn) prepareVMRuntimeV1(ctx context.Context, probe api.CapProbe, progress io.Writer, original *GrantRequest) (supply.Manifest, error) {
	if original == nil {
		request, err := runtimeRequestFor(probe.Arch)
		if err != nil {
			return supply.Manifest{}, err
		}
		if _, err := EnsureRuntime(ctx, c, RuntimeOptions{Probe: &probe, Progress: progress}); err != nil {
			return supply.Manifest{}, err
		}
		if _, err := runtimeReady(ctx, c, request); err != nil {
			return supply.Manifest{}, err
		}
		return cloneNewRuntimeManifest(request.Manifest), nil
	}
	if original != grantRequestOf(ctx) {
		return supply.Manifest{}, grantOperationError()
	}
	if err := requireVMOriginalRequestV1(ctx, c); err != nil {
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

// The existing issuer accepts Resume only from its actual retained cold plan.
func requireVMOriginalRequestV1(ctx context.Context, c *Conn) error {
	if requestGrantOperation(grantRequestOf(ctx)) == grantOpResume {
		return requireResumeVMProducerV1(ctx, c)
	}
	return requireGrantOperation(ctx, grantOpNew)
}

// Only the actual builder retained this full request before its projection.
// Returning data never supplies the host's original producer or its lifetime.
func (p *VMNeedPlanV1) runtimeOriginForV1(ctx context.Context, c *Conn) (json.RawMessage, error) {
	if _, err := p.EncodedFor(c); err != nil {
		return nil, err
	}
	if ctx == nil || p.stamp != c.hostTransport || p.request != grantRequestOf(ctx) {
		return nil, grantOperationError()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	return api.EncodeRuntimeOriginV1(api.RuntimeOriginV1{V: 1, Manifest: cloneNewRuntimeManifest(p.originalRuntime)})
}
