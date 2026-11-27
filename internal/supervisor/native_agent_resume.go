package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

// resumeAgentForNativeScopeV1 never starts or stops a runtime. The actual
// inspected controller remains the owner of the guest, egress and named agent.
func resumeAgentForNativeScopeV1(ctx context.Context, scope *nativeStopScopeV1, a api.ResumeArgs) (*api.ResumeResult, error) {
	if scope == nil || scope.root == nil || !api.ValidSessionID(a.ID) || a.NamedSSHKeys != nil || a.Inputs != nil {
		return nil, execRefusal("a warm agent resume cannot replace sandbox inputs or named keys")
	}
	root := scope.root
	root.spawnMu.Lock()
	defer root.spawnMu.Unlock()
	if err := scope.checkCurrentV1(ctx); err != nil {
		return nil, err
	}
	if a.Snapshot != nil {
		if err := host.CheckSandboxSnapshotLocked(a.ID, root.id, a.Snapshot); err != nil {
			return nil, err
		}
	}
	if err := root.requireConfirmedStoreBatch(); err != nil {
		return nil, err
	}
	selected := root.member(a.ID)
	if selected == nil {
		return nil, execRefusal("the selected session is not a member of the original warm controller")
	}
	root.mu.Lock()
	parent := root.snapshot()
	root.mu.Unlock()
	selected.mu.Lock()
	saved := selected.snapshot()
	previous := selected.runToken
	selected.mu.Unlock()
	if previous == "" {
		return nil, execRefusal("the selected member has no original owned run")
	}
	next, why, err := host.PrepareWarmAgentResumeV1(&saved, &parent, a.Environment)
	if err != nil {
		return nil, err
	}
	values, delivery := a.Secrets, a.SecretDelivery
	protected := a.ProtectedTrustedSecretValues
	if selected != root {
		values, delivery = nil, nil
		protected = nil
		found := false
		for _, member := range a.Members {
			if member.ID == a.ID {
				if found {
					return nil, secretIntentFailure()
				}
				found, values, delivery = true, member.Secrets, member.SecretDelivery
				protected = member.ProtectedTrustedSecretValues
			}
		}
		if !found {
			return nil, secretIntentFailure()
		}
	}
	for name := range values {
		if !slices.Contains(next.Secrets, name) || api.CheckSecretName(name, false) != api.SecretNameOK {
			return nil, secretIntentFailure()
		}
	}
	token, err := newRunToken()
	if err != nil {
		return nil, err
	}
	budget := newActiveBootBudget(ctx, bootTimeout)
	defer budget.close()
	budget.publisher = selected
	args := proto.CtlStartArgs{RunToken: token, Session: selected.id, Agent: next.Agent.Kind, Argv: slices.Clone(next.Agent.Argv), Env: slices.Clone(next.Env), Cwd: next.Cwd, Cols: next.Term.Cols, Rows: next.Term.Rows, Hooks: slices.Clone(next.Agent.Hooks)}
	if host.IsNoneSupervisor(next) {
		args.Argv = host.UnwalledStartArgv(next)
	}
	args.SSHAgent = root.namedSSHAgentEnabled()
	root.bootDecoyArgs(&args)
	if err := root.bootPackageSettingsArgs(&args); err != nil {
		return nil, err
	}
	shellStartArgs(next, &args)
	generation := ""
	if parent.SecretState != nil {
		generation = parent.SecretState.Generation
	}
	policyDelivery, err := warmTrustedDeliveryV1(delivery, protected, parent.SecretState)
	if err != nil {
		return nil, err
	}
	scope.markWarmMutationV1()
	environment, err := root.installMemberPolicy(budget, next, values, policyDelivery, generation, protected)
	if err != nil {
		return nil, err
	}
	defer clear(environment.Env)
	args.Secrets, args.SecretModes, args.Missing = environment.Env, environment.Modes, environment.Missing
	root.mu.Lock()
	hasCA := root.eg != nil && root.eg.caCert != ""
	proxy := guestProxyForDriver(root.m.Wall.Driver)
	root.mu.Unlock()
	if err := root.planMemberStores([]proto.CtlStartArgs{args}, hasCA, proxy); err != nil {
		return nil, err
	}
	if err := scope.checkCurrentV1(ctx); err != nil {
		return nil, err
	}
	if err := root.prepareWarmMemberV1(budget, &args, previous); err != nil {
		return nil, err
	}
	// Retirement ACK follows actual guest holder/PTY/event joins. Only now may
	// the same supervisor accept events for this new, independently named run.
	selected.evMu.Lock()
	selected.mu.Lock()
	selected.m.Agent.Argv, selected.m.Env = slices.Clone(next.Agent.Argv), slices.Clone(next.Env)
	selected.m.Term.Term = next.Term.Term
	selected.runToken, selected.end = token, endFacts{}
	selected.ready, selected.early, selected.earlyBytes = false, nil, 0
	selected.earlyEnd, selected.earlyCapture = false, false
	selected.mu.Unlock()
	selected.evMu.Unlock()
	if err := selected.installPublicSecretView(delivery); err != nil {
		return nil, errors.Join(err, root.abortPreparedMember(args, proto.CtlSpawn))
	}
	if err := scope.checkCurrentV1(ctx); err != nil {
		return nil, errors.Join(err, root.abortPreparedMember(args, proto.CtlSpawn))
	}
	var result proto.CtlStartResult
	err = root.ctl.requestDetached(budget, proto.CtlSpawn, args, &result)
	if err == nil && !root.guestABI(result.GuestABI) {
		err = execRefusal("the resumed guest PTY ownership schema does not match")
	}
	if err != nil {
		root.failMember()
		if controlWasNotSent(err) {
			err = errors.Join(err, root.abortPreparedMember(args, proto.CtlSpawn))
		}
		return nil, err
	}
	if err := selected.recordWarmAgentV1(result, environment.Missing); err != nil {
		return nil, err
	}
	if err := scope.checkCurrentV1(scope.lifetime); err != nil {
		return nil, err
	}
	root.mu.Lock()
	parent = root.snapshot()
	root.mu.Unlock()
	selected.mu.Lock()
	publication := selected.snapshot()
	selected.mu.Unlock()
	row, err := host.MemberPublicationInfo(&publication, true, &parent)
	if err != nil {
		return nil, err
	}
	return &api.ResumeResult{Session: *row, Why: why}, nil
}

func (s *Supervisor) prepareWarmMemberV1(b *activeBootBudget, args *proto.CtlStartArgs, previous string) (returned error) {
	if err := s.publishActiveBudget(b); err != nil {
		return err
	}
	if err := s.requirePreparedGuest(b, true); err != nil {
		return err
	}
	if err := proto.CheckMemberPrepareFrames(proto.CtlSpawn, *args); err != nil {
		return err
	}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, s.abortPreparedMember(*args, proto.CtlSpawn))
		}
	}()
	var raw json.RawMessage
	if err := s.ctl.requestDetached(b, proto.CtlMemberResumePrepare, proto.CtlMemberResumePrepareArgs{Version: 1, PreviousRun: previous, Start: *args}, &raw); err != nil {
		return err
	}
	var prepared proto.CtlMemberPrepareResult
	if !api.ExactJSONFields(raw, prepared) || json.Unmarshal(raw, &prepared) != nil || prepared.Version != 1 || !api.InputNonce(prepared.Claim).Valid() {
		return execRefusal("the warm member preparation claim is invalid")
	}
	args.PreparedClaim = prepared.Claim
	if !s.memberStores[args.Session] {
		return nil
	}
	if s.storeBatch == nil {
		return secretIntentFailure()
	}
	store := func(ctx context.Context) error {
		var raw json.RawMessage
		err := s.ctl.requestDetached(ctx, proto.CtlMemberStore, proto.CtlMemberPrepareArgs{Version: 1, Target: proto.CtlSpawn, Start: *args}, &raw)
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			fields, decode := api.JSONObjectFields(raw)
			if decode != nil || len(fields) != 0 {
				err = execRefusal("the warm member Store acknowledgement is invalid")
			}
		}
		if err != nil {
			s.failMember()
			return err
		}
		s.storeBatch.acknowledged = true
		return nil
	}
	if s.storeBatch.allocated {
		return store(b)
	}
	s.storeBatch.allocated = true
	return s.separatePhase(b, phaseclock.Current.Now().Add(memberStoreLimit), store)
}

// recordWarmAgentV1 updates only the selected agent. It never recommits the
// original native startup, resets max-run or changes the runtime generation.
func (s *Supervisor) recordWarmAgentV1(result proto.CtlStartResult, missing []string) error {
	t := now().UTC()
	s.mu.Lock()
	s.launched = true
	s.m.Guest = &host.GuestClaims{AgentPID: max(result.AgentPID, 0), At: t}
	s.m.MissingSecrets = slices.Clone(missing)
	s.m.Status = host.Status{State: host.StateWorking, Since: t, Source: "resume"}
	s.m.DebugUntil = nil
	s.rec.sorted, s.rec.oomAgent = false, false
	s.turn = turnClock{since: t}
	snapshot := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snapshot, false); err != nil {
		return err
	}
	s.registerLaunch()
	s.replay()
	s.raise(s.alertEvent(alerts.Started))
	s.audit.write("agent_resumed", map[string]any{"session": s.id})
	return nil
}
