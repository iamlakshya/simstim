package tui

import (
	"context"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type fakePrepareState struct {
	hostGen          map[string]int
	capProbe         *api.CapProbe
	prepareProbeErr  error
	prepareRuntime   api.RuntimeStatus
	prepareRtErr     error
	rootPreview      *client.HostPrepareRootPreviewV1
	inspectRootErr   error
	rootActionErr    error
	rootSealErr      error
	rootRunErr       error
	rootRunHook      func()
	prepareBootErr   error
	prepareNoWall    bool
	prepareBootLevel string
	prepareAfterRoot string
}

func (b *fakeBackend) bumpHostGenLocked(c client.HostChange) {
	if b.hostGen == nil {
		b.hostGen = map[string]int{}
	}
	if c.Was != nil && c.Now == nil {
		b.hostGen[c.Was.Name]++
		return
	}
	if c.Now != nil {
		if c.Was == nil {
			b.hostGen[c.Now.Name]++
		}
	}
}

func (b *fakeBackend) checkPrepareTarget(ctx context.Context, target *wizardPrepareTarget) (client.Host, client.Tier, error) {
	if err := ctx.Err(); err != nil {
		return client.Host{}, client.TierUnknown, err
	}
	if target == nil {
		return client.Host{}, client.TierUnknown, &client.UserError{
			Msg: "sandbox preparation lost its saved target", Hint: "Add or prepare the host again.",
			Code: "host_changed", Kind: client.KindRefused,
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	gen := b.hostGen[target.host.Name]
	var cur *client.Host
	for i := range b.hosts {
		if b.hosts[i].Name == target.host.Name {
			h := b.hosts[i]
			cur = &h
			break
		}
	}
	if cur == nil || gen != target.gen {
		return client.Host{}, client.TierUnknown, &client.UserError{
			Msg: target.host.Name + " changed while preparation was running", Hint: "Run preparation again.",
			Code: "host_changed", Kind: client.KindRefused,
		}
	}
	tier := client.TierOwn
	if e, ok := b.tiers[cur.Name]; ok && e.Tier.Valid() {
		tier = e.Tier
	}
	return *cur, tier, nil
}

func fakeWallRank(level string) int {
	switch level {
	case "process":
		return 1
	case "gvisor":
		return 2
	case "microvm":
		return 3
	default:
		return 0
	}
}

func (b *fakeBackend) defaultCapProbe() api.CapProbe {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capProbe != nil {
		return *b.capProbe
	}
	arch := b.probe.Arch
	if arch == "" {
		arch = "arm64"
	}
	return api.CapProbe{
		OS: "linux", Arch: arch, Kernel: "6.8.0", User: "pi", UID: 1000, UserNS: true, Sudo: true,
		Walls: []api.WallProbe{{Driver: "gvisor", Level: "gvisor", OK: true, CanPrepare: true, Version: "runsc"}},
	}
}

func (b *fakeBackend) UpdateHostsForPrepare(ctx context.Context, c client.HostChange) (*wizardPrepareTarget, error, error) {
	warn, err := b.UpdateHostsContext(ctx, c)
	if err != nil || c.Now == nil {
		return nil, warn, err
	}
	b.mu.Lock()
	gen := b.hostGen[c.Now.Name]
	var saved client.Host
	for _, h := range b.hosts {
		if h.Name == c.Now.Name {
			saved = h
			break
		}
	}
	b.mu.Unlock()
	return &wizardPrepareTarget{host: saved, gen: gen}, warn, nil
}

func (b *fakeBackend) PrepareProbe(ctx context.Context, target *wizardPrepareTarget) (api.CapProbe, string, error) {
	h, _, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return api.CapProbe{}, "", err
	}
	b.record("prepare probe %s", h.Name)
	b.mu.Lock()
	perr := b.prepareProbeErr
	b.mu.Unlock()
	if perr != nil {
		return api.CapProbe{}, "", perr
	}
	return b.defaultCapProbe(), "host", nil
}

func (b *fakeBackend) PrepareRuntime(ctx context.Context, target *wizardPrepareTarget, _ client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
	h, _, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return api.RuntimeStatus{}, err
	}
	b.record("prepare runtime %s", h.Name)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.prepareRuntime.Drivers) == 0 && b.prepareRtErr == nil {
		arch := b.probe.Arch
		if arch == "" {
			arch = "arm64"
		}
		return api.RuntimeStatus{
			Drivers: []api.RuntimeDriver{
				{Name: "gvisor", Arch: arch, Verified: true},
				{Name: "chv", Arch: arch, Verified: true},
			},
		}, nil
	}
	return b.prepareRuntime, b.prepareRtErr
}

func (b *fakeBackend) InspectPrepareRoot(ctx context.Context, target *wizardPrepareTarget) (client.HostPrepareRootPreviewV1, error) {
	h, tier, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return client.HostPrepareRootPreviewV1{}, err
	}
	b.record("inspect root %s", h.Name)
	b.mu.Lock()
	ierr := b.inspectRootErr
	custom := b.rootPreview
	b.mu.Unlock()
	if ierr != nil {
		return client.HostPrepareRootPreviewV1{}, ierr
	}
	probe := b.defaultCapProbe()
	if custom != nil {
		out := *custom
		if out.Tier == client.TierUnknown {
			out.Tier = tier
		}
		if out.Probe.OS == "" {
			out.Probe = probe
		}
		if out.DefaultMode == "" {
			out.DefaultMode = client.DefaultRootStepMode(out.Tier, out.Started, out.Probe, false)
		}
		return out, nil
	}
	return client.HostPrepareRootPreviewV1{Probe: probe, Tier: tier, HostID: "0123456789abcdef0123456789abcdef"}, nil
}

func (b *fakeBackend) FinishPrepareRoot(ctx context.Context, target *wizardPrepareTarget, mode client.RootStepMode, _ client.PrepareGrantFuncV1) (client.HostPrepareRootOutcomeV1, error) {
	h, tier, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return client.HostPrepareRootOutcomeV1{}, err
	}
	b.record("root %s %s", mode, h.Name)
	b.mu.Lock()
	aerr := b.rootActionErr
	b.mu.Unlock()
	if aerr != nil {
		return client.HostPrepareRootOutcomeV1{}, aerr
	}
	out := client.HostPrepareRootOutcomeV1{
		Tier: tier, Needed: true, Mode: mode,
		Printed: mode == client.RootPrint, Skipped: mode == client.RootSkip,
	}
	if mode == client.RootPrint {
		out.ScriptFile = "~/.cache/root-step.sh"
	}
	return out, nil
}

type fakePreparedRootRun struct {
	be     *fakeBackend
	host   string
	closed bool
}

func (r *fakePreparedRootRun) RunAndClose(*os.File, io.Writer) error {
	if r.closed {
		return &client.UserError{Msg: "the root step was already closed", Hint: "Try again.", Code: "root_closed", Kind: client.KindFailed}
	}
	r.closed = true
	r.be.record("root run %s", r.host)
	r.be.mu.Lock()
	hook, err := r.be.rootRunHook, r.be.rootRunErr
	r.be.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (r *fakePreparedRootRun) Close() error {
	r.closed = true
	return nil
}

func (b *fakeBackend) SealPrepareRootRun(ctx context.Context, target *wizardPrepareTarget, ask client.PrepareGrantFuncV1) (preparedRootRunner, error) {
	h, tier, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	b.record("root seal %s", h.Name)
	b.mu.Lock()
	serr := b.rootSealErr
	b.mu.Unlock()
	if serr != nil {
		return nil, serr
	}
	if tier == client.TierShared && ask != nil {
		req := client.NewRootStepGrantRequest()
		defer req.Close()
		gctx := client.WithGrantRequest(ctx, req)
		prompted := false
		_, err := ask(gctx, func(context.Context) error {
			if !prompted {
				prompted = true
				return &client.ConfirmError{
					Host:   h.Name,
					Kind:   client.ConfirmHostName,
					Prompt: "Run the root step on " + h.Name + "?",
					Err: &client.UserError{
						Msg:  "a live grant answer is needed",
						Hint: "Type the saved host name in the launcher.",
						Code: "tier_refused",
						Kind: client.KindRefused,
					},
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return &fakePreparedRootRun{be: b, host: h.Name}, nil
}

func (b *fakeBackend) BootPrepareSandbox(ctx context.Context, target *wizardPrepareTarget, opts client.HostPrepareOptionsV1, _ client.PrepareGrantFuncV1) (sandboxBootSummary, error) {
	h, _, err := b.checkPrepareTarget(ctx, target)
	if err != nil {
		return sandboxBootSummary{}, err
	}
	b.record("prepare boot %s", h.Name)
	probe := b.defaultCapProbe()
	b.mu.Lock()
	berr, noWall, level, afterRoot := b.prepareBootErr, b.prepareNoWall, b.prepareBootLevel, b.prepareAfterRoot
	b.mu.Unlock()
	if berr != nil {
		return sandboxBootSummary{}, berr
	}
	if noWall {
		return sandboxBootSummary{
			noRunnableWall: true, observedLevel: "none", levelAfterRoot: afterRoot,
			rootPrinted: opts.RootPrinted, probe: probe, saved: h,
		}, nil
	}
	if level == "" {
		level = "gvisor"
	}
	b.mu.Lock()
	saved := h
	for i := range b.hosts {
		if b.hosts[i].Name == h.Name {
			floor := level
			if fakeWallRank(b.hosts[i].WallFloor) > fakeWallRank(floor) {
				floor = b.hosts[i].WallFloor
			}
			b.hosts[i].WallFloor = floor
			b.hosts[i].Prepared = &client.PreparedHost{Level: level, Arch: probe.Arch, MemTotal: 4 << 30}
			saved = b.hosts[i]
			break
		}
	}
	b.mu.Unlock()
	return sandboxBootSummary{
		tested: true, observedLevel: level, levelAfterRoot: afterRoot,
		rootPrinted: opts.RootPrinted,
		result:      api.SandboxPrepareResultV1{Level: level, Driver: level},
		probe:       probe, saved: saved,
	}, nil
}
