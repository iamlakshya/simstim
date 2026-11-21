package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"github.com/iamlakshya/simstim/internal/wall/none"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// startGuest builds and starts the sandbox, opens the ctl stream, and
// starts the session in it. It records the run in the trusted manifest.
func (s *Supervisor) startGuest(ctx context.Context, sb sandbox, secrets map[string]string) error {
	budget, ok := ctx.(*activeBootBudget)
	if !ok {
		budget = newActiveBootBudget(ctx, s.bootLimit())
		defer budget.close()
		ctx = budget
	}
	s.bootBudget = budget
	budget.publisher = s
	if ctx.Err() != nil {
		return wallError("the boot ended before the sandbox started", ctx.Err())
	}
	if err := s.startPendingNamedSSHKeys(); err != nil {
		return err
	}
	if err := s.prepareBootDecoys(ctx); err != nil {
		return err
	}
	if err := s.prepareBootPackageSettings(ctx); err != nil {
		return err
	}
	modern := s.modernSecretStartup()
	unwalled := s.unwalledV1()
	if unwalled {
		modern = false
		defer s.clearStartupValues(secrets)
		defer s.clearNoneMemberValuesV1()
	}
	var environment secretEnvironment
	if unwalled {
		var err error
		environment, err = s.startNoneEnvironmentV1(ctx, secrets)
		if err != nil {
			return err
		}
		defer func() { clear(environment.Env) }()
	} else if modern {
		var err error
		environment, err = s.startEgress(ctx, secrets)
		if err != nil {
			return err
		}
	}
	s.adoptFileCapture(sb)
	s.mu.Lock()
	s.sb = sb
	s.sandboxStartAttempted = true
	s.mu.Unlock()
	conn, err := sb.Start(ctx)
	if err != nil {
		return err
	}
	if err := s.retainVMStart(ctx, sb, conn); err != nil {
		_ = conn.Close()
		return err
	}
	if runtime, ok := sb.(processSandbox); ok {
		owner, err := process.NativeCopyOwnerV1(runtime.Sandbox, conn)
		if err != nil {
			_ = conn.Close()
			return err
		}
		s.processCopy = owner
		gvisorOwner, err := gvisor.NativeLiveOwnerV1(runtime.Sandbox, conn)
		if err != nil {
			_ = conn.Close()
			return err
		}
		s.gvisorLive = gvisorOwner
	}
	if runtime, ok := sb.(noneRuntime); ok {
		owner, err := none.NativeLiveOwnerV1(runtime.Sandbox, conn)
		if err != nil {
			_ = conn.Close()
			return err
		}
		s.noneLive = owner
		receipt := host.OriginalVMNeedFromContext(s.lifetimeContext())
		if receipt == nil {
			_ = conn.Close()
			return execRefusal("the none creator has no original native startup receipt")
		}
		if err := receipt.BindOriginalNoneBirthV1(ctx, owner, conn); err != nil {
			_ = conn.Close()
			return err
		}
	}
	s.link = link.New(conn, link.Supervisor, link.Options{})
	st, err := s.link.Open(ctx, link.KindCtl, nil)
	if err != nil {
		return wallError("the sandbox's guest init did not answer", err)
	}
	s.ctl = newCtl(s, st)
	if err := s.requireDecoyGuest(ctx); err != nil {
		return err
	}
	if err := s.requirePackageManagerGuest(ctx); err != nil {
		return err
	}
	if err := s.requireNamedSSHGuest(ctx); err != nil {
		return err
	}
	if err := s.checkControllerPrepare(ctx); err != nil {
		return err
	}
	if modern {
		if err := s.requireBoundGuest(ctx); err != nil {
			s.stopEgress()
			return err
		}
	} else if !unwalled {
		environment, err = s.startEgress(ctx, secrets)
		if err != nil {
			return err
		}
	}
	if err := s.startGuestStreamReaderV1(); err != nil {
		return err
	}
	s.mu.Lock()
	m := s.m
	args := proto.CtlStartArgs{RunToken: s.runToken, PendingMembers: len(s.savedMembers), Session: s.id, Agent: m.Agent.Kind, Argv: m.Agent.Argv, Env: m.Env, Cwd: m.Cwd,
		Cols: m.Term.Cols, Rows: m.Term.Rows, Hooks: m.Agent.Hooks, Secrets: environment.Env, SecretModes: environment.Modes, Missing: environment.Missing, Restored: host.WasRestored(m)}
	if m.ForkPending {
		args.ForkOf = m.ForkBranchOf
		if args.ForkOf == "" {
			args.ForkOf = m.ForkOf
		}
	}
	if !unwalled {
		args.Proxy = guestProxyForDriver(m.Wall.Driver)
		args.DNS = dnsModeForManifest(m)
	} else {
		args.Argv = host.UnwalledStartArgv(m)
	}
	if s.eg != nil {
		args.CACert = s.eg.caCert
	}
	shellStartArgs(m, &args)
	// The holder caps the size, and its log says so. The trusted manifest
	// keeps the size that the agent gets, as the holder's own does.
	if m.Term.Cols > 0 || m.Term.Rows > 0 {
		m.Term.Cols, m.Term.Rows = host.CapTerm(orDefault(m.Term.Cols, 80), orDefault(m.Term.Rows, 24))
	}
	s.mu.Unlock()
	args.SSHAgent = s.namedSSHAgentEnabled()
	s.bootDecoyArgs(&args)
	if err := s.bootPackageSettingsArgs(&args); err != nil {
		return err
	}
	if s.capture != nil {
		plan, err := host.SandboxStatePlan(m)
		if err != nil {
			return err
		}
		args.StateFiles = plan.Files
		args.CaptureNonce = s.capture.Nonce
	}
	if err := s.prepareVMGuest(ctx, false); err != nil {
		return err
	}
	if s.vmPlan != nil {
		args.StateFiles = append([]string(nil), s.vmPlan.Args.StateFiles...)
		args.CaptureNonce = s.vmPlan.Args.CaptureNonce
	}
	if err := s.planSavedMembers(ctx, s.memberValues); err != nil {
		return err
	}
	selected := make([]proto.CtlStartArgs, 0, len(s.memberBootPlans)+1)
	if s.automatic == nil || s.automatic.PrimaryWanted {
		selected = append(selected, args)
	}
	for _, member := range s.memberBootPlans {
		selected = append(selected, member.args)
	}
	if err := s.planMemberStores(selected, args.CACert != "", args.Proxy); err != nil {
		return err
	}
	if err := s.seedWorkspace(ctx, m); err != nil {
		return err
	}
	if err := s.applyStartupInputs(ctx, &args); err != nil {
		return err
	}
	if s.automatic != nil && !s.automatic.PrimaryWanted {
		return s.prepareController(ctx, args)
	}
	if err := s.prepareMemberPhases(s.bootBudget, &args, proto.CtlStart, false); err != nil {
		return err
	}
	if !s.isSandboxPrepareBootV1() {
		s.registerLaunch()
	}
	var res proto.CtlStartResult
	if err := s.ctl.request(ctx, proto.CtlStart, args, &res); err != nil {
		return err
	}
	if !s.guestABI(res.GuestABI) {
		return execRefusal("the guest PTY ownership schema does not match this runtime")
	}
	if err := s.markPackageSettingsInstalled(); err != nil {
		return err
	}
	s.mu.Lock()
	s.launched = true // actual start ACK, before optional boot certification
	s.mu.Unlock()
	if err := s.certifyGuestBoot(ctx, res.GuestABI); err != nil {
		return err
	}
	s.terminalABI = res.GuestABI
	if err := s.restoreSavedMembers(ctx, s.memberValues); err != nil {
		return err
	}
	if err := s.started(res, args.Missing); err != nil {
		return err
	}
	return s.automaticReady()
}
