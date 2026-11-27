package client

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// HostPrepareRootOptionsV1 configures Phase 3 (optional root step) on target.
type HostPrepareRootOptionsV1 struct {
	ExplicitMode     RootStepMode
	DelegateIO       bool
	SeparateAccounts bool
	NitroSlots       int
	Yes              bool
	Wake             bool
	Interactive      bool
	In               *os.File
	Out              io.Writer
	ShowScript       func(RootScript)
	AskGrant         PrepareGrantFuncV1
	AuthoriseRun     func(h Host, tier Tier, probe api.CapProbe, explicit, yes bool) error
}

// HostPrepareRootOutcomeV1 reports what Phase 3 observed or recorded.
type HostPrepareRootOutcomeV1 struct {
	Probe      api.CapProbe
	Tier       Tier
	HostID     string
	Needed     bool
	Mode       RootStepMode
	Ran        bool
	Printed    bool
	Skipped    bool
	ScriptFile string
	Script     RootScript
	SHA256     string
}

func verifyRootTargetIdentityV1(ctx context.Context, target *HostPrepareTargetV1, conn *Conn) (Tier, string, error) {
	if err := target.checkConn(ctx, conn); err != nil {
		return TierUnknown, "", err
	}
	tier, hostID, err := HostGrantTier(ctx, target.Host())
	if err != nil {
		return TierUnknown, "", err
	}
	if !ValidHostID(hostID) || hostID != conn.HostID() {
		return TierUnknown, "", &UserError{
			Msg:  "the host identity changed before its root step",
			Hint: brand.Name + " host test " + ShellQuote(target.Name()),
			Code: "root_step_identity",
			Kind: KindRefused,
		}
	}
	return tier, hostID, nil
}

func buildPrepareRootScriptV1(ctx context.Context, conn *Conn, probe api.CapProbe, delegateIO, separateAccounts bool, slots ...int) ([]RootStepID, RootScript, bool, error) {
	nitroSlots := 0
	if len(slots) == 1 {
		nitroSlots = slots[0]
	}
	if len(slots) > 1 || nitroSlots < 0 || nitroSlots > 1<<20 {
		return nil, RootScript{}, false, rootStepError("the Nitro slot target is invalid")
	}
	ids := RootStepsFor(probe, api.RuntimeStatus{}, delegateIO, nitroSlots)
	if nitroSlots > 0 && (probe.Nitro == nil || !probe.Nitro.Present || !probe.HasSetfacl || int64(nitroSlots) > probe.Nitro.Pools["pcie_slots_total"]) {
		return nil, RootScript{}, false, rootStepError("the Nitro slot target needs a supported device, setfacl and room in this instance")
	}
	if !slices.Contains(ids, RootNitroSlots) {
		nitroSlots = 0
	}
	if len(ids) == 0 && !separateAccounts {
		return nil, RootScript{}, false, nil
	}
	var script RootScript
	var err error
	if separateAccounts {
		script, err = conn.PrepareAccountRootScript(ctx, ids, separateAccounts, nitroSlots)
	} else {
		script, err = BuildRootScript(probe.User, probe.UID, ids, nitroSlots)
	}
	if err != nil {
		return nil, RootScript{}, false, err
	}
	recorded := make([]RootStepID, 0, len(script.Steps))
	for _, step := range script.Steps {
		recorded = append(recorded, step.ID)
	}
	return recorded, script, true, nil
}

// RunHostPrepareRootPhaseV1 runs Phase 3 under its own NewRootStepGrantRequest
// and dedicated saved-host connection, closing both before Phase 4 begins.
func RunHostPrepareRootPhaseV1(ctx context.Context, target *HostPrepareTargetV1, opts HostPrepareRootOptionsV1) (out HostPrepareRootOutcomeV1, returned error) {
	if target == nil {
		return out, sandboxPrepareError("root-step preparation needs its original saved target")
	}
	if opts.ExplicitMode != "" {
		if _, err := ParseRootStepMode(string(opts.ExplicitMode)); err != nil {
			return out, err
		}
	}
	req := NewRootStepGrantRequest()
	phaseCtx := WithGrantRequest(ctx, req)
	defer func() { returned = errors.Join(returned, req.Close()) }()

	conn, err := dialPrepareTargetV1(phaseCtx, target, opts.Wake, opts.Interactive && !opts.Yes)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(phaseCtx, "")
	if err != nil {
		return out, err
	}
	tier, hostID, err := verifyRootTargetIdentityV1(phaseCtx, target, conn)
	if err != nil {
		return out, err
	}
	out.Probe, out.Tier, out.HostID = CleanCapProbe(probe), tier, hostID
	ids, script, needed, err := buildPrepareRootScriptV1(phaseCtx, conn, probe, opts.DelegateIO, opts.SeparateAccounts, opts.NitroSlots)
	if err != nil || !needed {
		return out, err
	}
	started, err := RootHistoryStarted(phaseCtx, target.Host(), hostID)
	if err != nil {
		return out, err
	}
	mode := opts.ExplicitMode
	if mode == "" {
		mode = DefaultRootStepMode(tier, started, probe, opts.Yes)
	}
	out.Needed, out.Mode, out.Script, out.SHA256 = true, mode, script, script.SHA256
	if opts.ShowScript != nil {
		opts.ShowScript(script)
	}
	grantCtx, err := runPrepareGrantV1(phaseCtx, opts.AskGrant, func(gctx context.Context) error {
		return conn.CheckRootStepGrant(gctx, script, mode, opts.Yes)
	})
	if err != nil {
		return out, err
	}
	if mode == RootRun {
		if opts.AuthoriseRun != nil {
			if err := opts.AuthoriseRun(target.Host(), tier, probe, opts.ExplicitMode != "", opts.Yes); err != nil {
				return out, err
			}
		}
		if err := conn.RunRootStep(grantCtx, script, mode, RootRunOptions{Passwordless: opts.Yes, In: opts.In, Out: opts.Out}); err != nil {
			return out, err
		}
		out.Ran = true
		return out, nil
	}
	status, file := "skipped", ""
	if mode == RootPrint {
		file, err = conn.WriteRootScript(grantCtx, script)
		if err != nil {
			return out, err
		}
		status = "printed"
	}
	if err := RecordRootStep(grantCtx, target.Host(), RootStepRecord{
		HostID: hostID, Steps: ids, SHA256: script.SHA256, Mode: mode,
		At: nowUTC(), Status: status, StartedSeen: started || probe.Started != 0,
	}); err != nil {
		return out, err
	}
	out.Printed, out.Skipped, out.ScriptFile = mode == RootPrint, mode == RootSkip, file
	return out, nil
}

// HostPrepareRootPreviewV1 holds non-mutating root-step inspection facts for
// the add-host wizard's Sandboxes and Root step screens.
type HostPrepareRootPreviewV1 struct {
	Probe       api.CapProbe
	Tier        Tier
	HostID      string
	Started     bool
	Needed      bool
	DefaultMode RootStepMode
	Script      RootScript
}

// InspectHostPrepareRootV1 inspects optional root steps for target without
// running or recording them, closing its connection and purpose before return.
func InspectHostPrepareRootV1(ctx context.Context, target *HostPrepareTargetV1, delegateIO, separateAccounts, wake bool) (preview HostPrepareRootPreviewV1, returned error) {
	if target == nil {
		return preview, sandboxPrepareError("root-step inspection needs its original saved target")
	}
	req := NewRootStepGrantRequest()
	phaseCtx := WithGrantRequest(ctx, req)
	defer func() { returned = errors.Join(returned, req.Close()) }()

	conn, err := dialPrepareTargetV1(phaseCtx, target, wake, false)
	if err != nil {
		return preview, err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(phaseCtx, "")
	if err != nil {
		return preview, err
	}
	tier, hostID, err := verifyRootTargetIdentityV1(phaseCtx, target, conn)
	if err != nil {
		return preview, err
	}
	preview.Probe, preview.Tier, preview.HostID = CleanCapProbe(probe), tier, hostID
	_, script, needed, err := buildPrepareRootScriptV1(phaseCtx, conn, probe, delegateIO, separateAccounts)
	if err != nil || !needed {
		return preview, err
	}
	started, err := RootHistoryStarted(phaseCtx, target.Host(), hostID)
	if err != nil {
		return preview, err
	}
	preview.Started = started
	preview.Needed = true
	preview.Script = script
	preview.DefaultMode = DefaultRootStepMode(tier, started, probe, false)
	return preview, nil
}

// PreparedRootRunV1 holds a sealed interactive root-step execution ready to
// hand terminal I/O to sudo, then close its owned connection and purpose.
type PreparedRootRunV1 struct {
	mu           sync.Mutex
	req          *GrantRequest
	ctx          context.Context
	conn         *Conn
	script       RootScript
	passwordless bool
	closed       bool
}

// SealHostPrepareRootRunV1 opens a dedicated root-step purpose and connection,
// checks sudo availability, and seals CheckRootStepGrant via askGrant before
// the launcher hands terminal I/O to RunAndClose.
func SealHostPrepareRootRunV1(ctx context.Context, target *HostPrepareTargetV1, delegateIO, separateAccounts, passwordless, wake bool, askGrant PrepareGrantFuncV1) (*PreparedRootRunV1, error) {
	if target == nil {
		return nil, sandboxPrepareError("root-step preparation needs its original saved target")
	}
	req := NewRootStepGrantRequest()
	phaseCtx := WithGrantRequest(ctx, req)
	conn, err := dialPrepareTargetV1(phaseCtx, target, wake, true)
	if err != nil {
		return nil, errors.Join(err, req.Close())
	}
	fail := func(cause error) (*PreparedRootRunV1, error) {
		conn.Close()
		return nil, errors.Join(cause, req.Close())
	}
	probe, err := conn.CapProbe(phaseCtx, "")
	if err != nil {
		return fail(err)
	}
	if _, _, err := verifyRootTargetIdentityV1(phaseCtx, target, conn); err != nil {
		return fail(err)
	}
	if !probe.Sudo {
		return fail(&UserError{
			Msg:  "this account has no sudo for its root step",
			Hint: brand.Name + " host prepare " + ShellQuote(target.Name()) + " --root-step print (give the script to the machine's owner)",
			Code: "root_step_sudo",
			Kind: KindFailed,
		})
	}
	_, script, needed, err := buildPrepareRootScriptV1(phaseCtx, conn, probe, delegateIO, separateAccounts)
	if err != nil {
		return fail(err)
	}
	if !needed {
		return fail(rootStepError("no supported root step is needed on this host"))
	}
	grantCtx, err := runPrepareGrantV1(phaseCtx, askGrant, func(gctx context.Context) error {
		return conn.CheckRootStepGrant(gctx, script, RootRun, passwordless)
	})
	if err != nil {
		return fail(err)
	}
	return &PreparedRootRunV1{req: req, ctx: grantCtx, conn: conn, script: script, passwordless: passwordless}, nil
}

// RunAndClose executes the sealed root step on the terminal and closes its
// owned connection and GrantRequest regardless of outcome.
func (p *PreparedRootRunV1) RunAndClose(in *os.File, out io.Writer) error {
	if p == nil {
		return rootStepError("the prepared root step is already closed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.conn == nil || p.req == nil {
		return rootStepError("the prepared root step is already closed")
	}
	err := p.conn.RunRootStep(p.ctx, p.script, RootRun, RootRunOptions{Passwordless: p.passwordless, In: in, Out: out})
	return errors.Join(err, p.closeLockedV1())
}

// Close releases the owned connection and GrantRequest without running.
func (p *PreparedRootRunV1) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLockedV1()
}

func (p *PreparedRootRunV1) closeLockedV1() error {
	if p.closed {
		return nil
	}
	p.closed = true
	if p.conn != nil {
		p.conn.Close()
	}
	if p.req != nil {
		return p.req.Close()
	}
	return nil
}
