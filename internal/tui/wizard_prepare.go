package tui

import (
	"context"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// wizardPrepareTarget carries the saved-host target returned by registration
// commit without re-resolving host names across phases.
type wizardPrepareTarget struct {
	real *client.HostPrepareTargetV1
	host client.Host
	gen  int
}

type preparedRootRunner interface {
	RunAndClose(in *os.File, out io.Writer) error
	Close() error
}

type sandboxBootSummary struct {
	tested         bool
	noRunnableWall bool
	observedLevel  string
	levelAfterRoot string
	rootPrinted    bool
	result         api.SandboxPrepareResultV1
	probe          api.CapProbe
	saved          client.Host
}

// sandboxPrepareBackend extends Backend for the 10-step wizard's Sandboxes
// and Root step screens while keeping older test wrapper backends compatible.
type sandboxPrepareBackend interface {
	UpdateHostsForPrepare(context.Context, client.HostChange) (*wizardPrepareTarget, error, error)
	PrepareProbe(context.Context, *wizardPrepareTarget) (api.CapProbe, string, error)
	PrepareRuntime(context.Context, *wizardPrepareTarget, client.PrepareGrantFuncV1) (api.RuntimeStatus, error)
	InspectPrepareRoot(context.Context, *wizardPrepareTarget) (client.HostPrepareRootPreviewV1, error)
	FinishPrepareRoot(context.Context, *wizardPrepareTarget, client.RootStepMode, client.PrepareGrantFuncV1) (client.HostPrepareRootOutcomeV1, error)
	SealPrepareRootRun(context.Context, *wizardPrepareTarget, client.PrepareGrantFuncV1) (preparedRootRunner, error)
	BootPrepareSandbox(context.Context, *wizardPrepareTarget, client.HostPrepareOptionsV1, client.PrepareGrantFuncV1) (sandboxBootSummary, error)
}

func canPrepareSandbox(be Backend) bool {
	_, ok := be.(sandboxPrepareBackend)
	return ok
}

func (realBackend) UpdateHostsForPrepare(ctx context.Context, c client.HostChange) (*wizardPrepareTarget, error, error) {
	name := changedName(c)
	ctx, unlock, err := client.LockHost(ctx, name, c.Kind)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	target, err := client.UpdateHostsForPreparation(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	return &wizardPrepareTarget{real: target, host: target.Host()}, nil, nil
}

func (realBackend) PrepareProbe(ctx context.Context, target *wizardPrepareTarget) (api.CapProbe, string, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	return client.ProbeHostPrepareV1(ctx, real, "", false, false)
}

func (realBackend) PrepareRuntime(ctx context.Context, target *wizardPrepareTarget, ask client.PrepareGrantFuncV1) (api.RuntimeStatus, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	return client.DeliverHostPrepareRuntimeV1(ctx, real, "", false, nil, ask)
}

func (realBackend) InspectPrepareRoot(ctx context.Context, target *wizardPrepareTarget) (client.HostPrepareRootPreviewV1, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	return client.InspectHostPrepareRootV1(ctx, real, false, false, false)
}

func (realBackend) FinishPrepareRoot(ctx context.Context, target *wizardPrepareTarget, mode client.RootStepMode, ask client.PrepareGrantFuncV1) (client.HostPrepareRootOutcomeV1, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	return client.RunHostPrepareRootPhaseV1(ctx, real, client.HostPrepareRootOptionsV1{
		ExplicitMode: mode,
		AskGrant:     ask,
	})
}

func (realBackend) SealPrepareRootRun(ctx context.Context, target *wizardPrepareTarget, ask client.PrepareGrantFuncV1) (preparedRootRunner, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	return client.SealHostPrepareRootRunV1(ctx, real, false, false, false, false, ask)
}

func (realBackend) BootPrepareSandbox(ctx context.Context, target *wizardPrepareTarget, opts client.HostPrepareOptionsV1, ask client.PrepareGrantFuncV1) (sandboxBootSummary, error) {
	var real *client.HostPrepareTargetV1
	if target != nil {
		real = target.real
	}
	outcome, saved, err := client.BootAndRecordHostPrepareV1(ctx, real, "", opts, false, false, ask)
	if err != nil || outcome == nil {
		return sandboxBootSummary{}, err
	}
	return sandboxBootSummary{
		tested:         outcome.Tested(),
		noRunnableWall: outcome.NoRunnableWall(),
		observedLevel:  outcome.ObservedLevel(),
		levelAfterRoot: outcome.LevelAfterRoot(),
		rootPrinted:    outcome.RootPrinted(),
		result:         outcome.Result(),
		probe:          outcome.Probe(),
		saved:          saved,
	}, nil
}

func (m *Model) askPrepareGrant(ctx context.Context, run func(context.Context) error) (context.Context, error) {
	confirmed := ctx
	err := m.grantQuestions(ctx, func(gctx context.Context) error {
		confirmed = gctx
		return run(gctx)
	})
	return confirmed, err
}

type sandboxPart struct {
	prepareAfterSave bool
	prepTarget       *wizardPrepareTarget
	sandboxChecks    []check
	inspected        bool
	capProbe         api.CapProbe
	rtStatus         api.RuntimeStatus
	rootPreview      client.HostPrepareRootPreviewV1
	rootPrinted      bool
	printedFile      string
	waitingRoot      bool
	sandboxTested    bool
	bootSummary      *sandboxBootSummary
}

type (
	sandboxInspectMsg struct {
		owner   *wizard
		seq     int
		probe   api.CapProbe
		runtime api.RuntimeStatus
		root    client.HostPrepareRootPreviewV1
		err     error
		took    time.Duration
	}
	rootSealedMsg struct {
		owner  *wizard
		seq    int
		run    preparedRootRunner
		cancel context.CancelFunc
		err    error
	}
	rootExecDoneMsg struct {
		owner *wizard
		seq   int
		err   error
	}
	rootActionDoneMsg struct {
		owner *wizard
		seq   int
		out   client.HostPrepareRootOutcomeV1
		err   error
	}
	sandboxBootMsg struct {
		owner   *wizard
		seq     int
		summary sandboxBootSummary
		err     error
		took    time.Duration
	}
)

func (w *wizard) startSandboxPrepare(m *Model, sb sandboxPrepareBackend) tea.Cmd {
	w.step, w.err, w.inspected = wzSandboxes, nil, false
	w.sandboxChecks = []check{
		{name: "probe", state: ckRunning, detail: "sandbox walls"},
		{name: "runtime", state: ckRunning, detail: "pinned images"},
	}
	ctx, seq := w.beginAgentOp(m)
	w.run = true
	target, clock := w.prepTarget, m.now
	return m.bgCmdFrom(ctx, func(opCtx context.Context) tea.Msg {
		start := clock()
		probe, _, err := sb.PrepareProbe(opCtx, target)
		if err != nil {
			return sandboxInspectMsg{owner: w, seq: seq, err: err, took: clock().Sub(start)}
		}
		rt, err := sb.PrepareRuntime(opCtx, target, m.askPrepareGrant)
		if err != nil {
			return sandboxInspectMsg{owner: w, seq: seq, probe: probe, err: err, took: clock().Sub(start)}
		}
		root, err := sb.InspectPrepareRoot(opCtx, target)
		return sandboxInspectMsg{owner: w, seq: seq, probe: probe, runtime: rt, root: root, err: err, took: clock().Sub(start)}
	})
}

func (w *wizard) onSandboxInspect(m *Model, msg sandboxInspectMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzSandboxes || !w.run {
		return nil
	}
	w.finishOp()
	w.run = false
	if msg.err != nil {
		for i := range w.sandboxChecks {
			if w.sandboxChecks[i].state == ckRunning {
				w.sandboxChecks[i].state = ckFailed
			}
		}
		w.err = msg.err
		return nil
	}
	w.inspected = true
	w.capProbe, w.rtStatus, w.rootPreview = client.CleanCapProbe(msg.probe), msg.runtime, msg.root
	w.sandboxChecks = buildSandboxChecks(w.capProbe, w.rtStatus, msg.took)
	if !w.rootPreview.Needed {
		return w.startTestBoot(m, false)
	}
	return nil
}

func (w *wizard) sandboxesKey(m *Model, ks string) tea.Cmd {
	if w.run {
		return nil
	}
	sb, ok := m.be.(sandboxPrepareBackend)
	if !ok {
		return nil
	}
	if w.err != nil {
		switch ks {
		case "enter":
			if !w.inspected {
				return w.startSandboxPrepare(m, sb)
			}
			return w.startTestBoot(m, w.rootPrinted)
		case "s":
			w.err, w.sandboxTested, w.step = nil, false, wzReady
			return w.loadAgents(m)
		}
		return nil
	}
	if w.rootPreview.Needed {
		switch ks {
		case "enter":
			w.step, w.err = wzRoot, nil
		case "s":
			return w.startRootAction(m, client.RootSkip)
		}
	}
	return nil
}

func (w *wizard) rootKey(m *Model, ks string) tea.Cmd {
	if w.run || w.waitingRoot {
		return nil
	}
	switch ks {
	case "enter":
		if w.rootPreview.DefaultMode == client.RootRun {
			return w.startRootRun(m)
		}
		return w.startRootAction(m, client.RootPrint)
	case "r":
		return w.startRootRun(m)
	case "p":
		return w.startRootAction(m, client.RootPrint)
	case "s":
		return w.startRootAction(m, client.RootSkip)
	}
	return nil
}

func (w *wizard) startRootRun(m *Model) tea.Cmd {
	sb, ok := m.be.(sandboxPrepareBackend)
	if !ok {
		return nil
	}
	ctx, seq := w.beginAgentOp(m)
	w.run, w.err = true, nil
	target := w.prepTarget
	return m.bgCmdFrom(ctx, func(opCtx context.Context) tea.Msg {
		sealCtx, sealCancel := context.WithCancel(m.ctx)
		stopLink := context.AfterFunc(opCtx, sealCancel)
		run, err := sb.SealPrepareRootRun(sealCtx, target, m.askPrepareGrant)
		if !stopLink() {
			if run != nil {
				_ = run.Close()
			}
			sealCancel()
			return rootSealedMsg{owner: w, seq: seq, err: context.Canceled}
		}
		if err != nil {
			sealCancel()
			return rootSealedMsg{owner: w, seq: seq, err: err}
		}
		return rootSealedMsg{owner: w, seq: seq, run: run, cancel: sealCancel}
	})
}

func (m *Model) onRootSealed(msg rootSealedMsg) tea.Cmd {
	w, ok := questionOwner(m.modal).(*wizard)
	if !ok || !w.owns(msg.owner, msg.seq) || w.step != wzRoot || !w.run {
		if msg.run != nil {
			_ = msg.run.Close()
		}
		if msg.cancel != nil {
			msg.cancel()
		}
		return nil
	}
	m.modal = w
	w.finishOp()
	w.run = false
	if msg.err != nil {
		if msg.cancel != nil {
			msg.cancel()
		}
		w.err = msg.err
		return nil
	}
	w.waitingRoot = true
	seq, run, cancel := msg.seq, msg.run, msg.cancel
	m.conns.onClose(func() {
		if cancel != nil {
			cancel()
		}
		_ = run.Close()
	})
	x := &funcExec{run: func(in, out *os.File) error {
		return run.RunAndClose(in, out)
	}}
	return m.execWithInput(x, func(err error) tea.Msg {
		if cancel != nil {
			cancel()
		}
		return rootExecDoneMsg{owner: w, seq: seq, err: err}
	})
}

func (w *wizard) onRootExecDone(m *Model, msg rootExecDoneMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzRoot || !w.waitingRoot {
		return nil
	}
	w.waitingRoot = false
	if msg.err != nil {
		w.err = msg.err
		return nil
	}
	w.rootPreview.Needed = false
	return w.startTestBoot(m, false)
}

func (w *wizard) startRootAction(m *Model, mode client.RootStepMode) tea.Cmd {
	sb, ok := m.be.(sandboxPrepareBackend)
	if !ok {
		return nil
	}
	ctx, seq := w.beginAgentOp(m)
	w.run, w.err = true, nil
	target := w.prepTarget
	return m.bgCmdFrom(ctx, func(opCtx context.Context) tea.Msg {
		out, err := sb.FinishPrepareRoot(opCtx, target, mode, m.askPrepareGrant)
		return rootActionDoneMsg{owner: w, seq: seq, out: out, err: err}
	})
}

func (w *wizard) onRootActionDone(m *Model, msg rootActionDoneMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || !w.run || (w.step != wzRoot && w.step != wzSandboxes) {
		return nil
	}
	w.finishOp()
	w.run = false
	if msg.err != nil {
		w.err = msg.err
		return nil
	}
	w.rootPreview.Needed = false
	w.rootPrinted = msg.out.Printed
	w.printedFile = shown(msg.out.ScriptFile)
	return w.startTestBoot(m, msg.out.Printed)
}

func (w *wizard) startTestBoot(m *Model, rootPrinted bool) tea.Cmd {
	sb, ok := m.be.(sandboxPrepareBackend)
	if !ok {
		return nil
	}
	w.step, w.err, w.rootPrinted = wzSandboxes, nil, rootPrinted
	w.sandboxChecks = upsertSandboxCheck(w.sandboxChecks, check{name: "test boot", state: ckRunning, detail: "verifying sandbox wall"})
	ctx, seq := w.beginAgentOp(m)
	w.run = true
	target, clock := w.prepTarget, m.now
	return m.bgCmdFrom(ctx, func(opCtx context.Context) tea.Msg {
		start := clock()
		sum, err := sb.BootPrepareSandbox(opCtx, target, client.HostPrepareOptionsV1{RootPrinted: rootPrinted}, m.askPrepareGrant)
		return sandboxBootMsg{owner: w, seq: seq, summary: sum, err: err, took: clock().Sub(start)}
	})
}

func (w *wizard) onSandboxBoot(m *Model, msg sandboxBootMsg) tea.Cmd {
	if !w.owns(msg.owner, msg.seq) || w.step != wzSandboxes || !w.run {
		return nil
	}
	w.finishOp()
	w.run = false
	if msg.err != nil {
		w.sandboxChecks = upsertSandboxCheck(w.sandboxChecks, check{name: "test boot", state: ckFailed, detail: "failed"})
		w.err = msg.err
		return nil
	}
	sum := msg.summary
	w.bootSummary = &sum
	w.sandboxTested = sum.tested
	if sum.tested {
		w.host = sum.saved
		if hc := m.hostByName(w.host.Name); hc != nil {
			hc.host = sum.saved
		}
		w.sandboxChecks = upsertSandboxCheck(w.sandboxChecks, check{name: "test boot", state: ckOK, detail: shown(sum.observedLevel) + " verified", took: msg.took})
	} else {
		w.sandboxChecks = upsertSandboxCheck(w.sandboxChecks, check{name: "test boot", state: ckNote, detail: "no runnable wall yet"})
	}
	w.step, w.err = wzReady, nil
	return w.loadAgents(m)
}
