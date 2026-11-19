package client

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// ResolveNewVMOptions uses the SAME policy/agent size rules as the actual
// sandbox. The spec's default disk/image values are selected before delivery;
// the lower Need builder never invents defaults or reduces a guest cap.
func ResolveNewVMOptions(a NewArgs, intent string) (VMNeedOptionsV1, error) {
	if intent != "auto" && intent != "microvm" {
		return VMNeedOptionsV1{}, vmPrepareUnavailable()
	}
	policy, _, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return VMNeedOptionsV1{}, policyError(err)
	}
	selected, err := agents.ResolveStart(a.Agent, a.Argv)
	if err != nil {
		return VMNeedOptionsV1{}, grantOperationError()
	}
	var defaults *api.SandboxSize
	if agent, known := agents.Find(selected.Kind); known {
		defaults = agent.SandboxSize()
	}
	size, err := api.ResolveSandboxSize(policy.Sandbox, nil, defaults)
	if err != nil {
		return VMNeedOptionsV1{}, err
	}
	disk := int64(20 << 30)
	if policy.Sandbox.DiskMax != nil {
		disk, err = api.ParseSize(*policy.Sandbox.DiskMax)
		if err != nil || disk <= 0 {
			return VMNeedOptionsV1{}, vmPrepareUnavailable()
		}
	}
	image := "default"
	if policy.Sandbox.Image != nil {
		image = *policy.Sandbox.Image
	}
	return VMNeedOptionsV1{Image: image, Intent: intent, GuestRAM: size.Memory, CPUs: size.CPUs, DiskMax: disk}, nil
}

// PrepareNewVM runs outside HostLock, before value commands. Only this actual
// builder's result may be supplied to PrepareNewSecrets for this SAME Conn.
func (c *Conn) PrepareNewVM(ctx context.Context, a NewArgs, intent string) (*VMNeedPlanV1, error) {
	if a.Wall != "microvm" {
		return nil, vmPrepareUnavailable()
	}
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	beforeVM, beforeIntent := r.preparedVM, r.preparedVMIntent
	r.mu.Unlock()
	if beforeVM != nil {
		if beforeIntent != intent {
			return nil, grantOperationError()
		}
		if _, err := beforeVM.EncodedFor(c); err != nil {
			return nil, err
		}
		if _, err := validateNewVMPlan(ctx, a, beforeVM); err != nil {
			return nil, err
		}
		if err := c.CheckVMFirstPrepareV1(ctx, beforeVM.Need(), intent); err != nil {
			return nil, err
		}
		return beforeVM, nil
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return nil, err
	}
	if hid != c.HostID() {
		return nil, grantIdentityError(c.Host)
	}
	if err := allowRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSilentUpgrade}, settings); err != nil {
		return nil, err
	}
	if a.Wall != "microvm" {
		return nil, vmPrepareUnavailable()
	}
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	options, err := ResolveNewVMOptions(a, intent)
	if err != nil {
		return nil, err
	}
	plan, err := c.PrepareVMNeedV1(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.preparationClosed || r.preparedVM != nil {
		r.mu.Unlock()
		return nil, grantOperationError()
	}
	r.preparedVM, r.preparedVMIntent = plan, intent
	r.mu.Unlock()
	return plan, nil
}

func validateNewVMPlan(ctx context.Context, a NewArgs, p *VMNeedPlanV1) (int64, error) {
	options, err := ResolveNewVMOptions(a, "auto")
	if err != nil {
		return 0, err
	}
	need := p.Need()
	if need.GuestRAM != options.GuestRAM || need.CPUs != options.CPUs || need.DiskMax != options.DiskMax {
		return 0, vmPrepareUnavailable()
	}
	selected, err := resolveImage(ctx, options.Image, need.Image.Arch, false, ImageOptions{Arch: need.Image.Arch})
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(selected.request, need.Image) {
		return 0, vmPrepareUnavailable()
	}
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		return 0, err
	}
	if a.MemoryMax > 0 && a.MemoryMax != budget.Limit {
		return 0, &UserError{Msg: "the unit memory cap differs from the selected microvm budget", Hint: "Use the sandbox memory setting so its guest and complete unit budget agree.", Code: "vm_expected", Kind: KindRefused}
	}
	return budget.Limit, nil
}
