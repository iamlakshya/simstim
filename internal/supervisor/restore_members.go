package supervisor

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) loadSavedMembers() error {
	rows, err := host.SandboxMembers(s.id)
	if err != nil {
		return err
	}
	for _, member := range rows {
		if err := knownSessionSecretState(member.SecretState); err != nil {
			return err
		}
	}
	s.knownMembers = rows
	s.savedMembers = rows
	s.expected = map[string]bool{}
	if s.automatic == nil || s.automatic.PrimaryWanted {
		s.expected[s.id] = true
	}
	if s.automatic != nil {
		s.savedMembers = nil
		for _, member := range rows {
			if slices.Contains(s.automatic.MemberIDs, member.ID) {
				s.savedMembers = append(s.savedMembers, member)
			}
		}
	}
	for _, m := range s.savedMembers {
		s.expected[m.ID] = true
	}
	return nil
}

type memberBootPlan struct {
	child *Supervisor
	args  proto.CtlStartArgs
}

// planSavedMembers fixes all selected arguments and sentinel projections before
// the first Store allocation. Invalid ordinary saved commands keep their
// existing warn-and-skip behaviour; automatic restoration remains closed.
func (s *Supervisor) planSavedMembers(ctx context.Context, values map[string]map[string]string) error {
	s.memberBootPlans = nil
	for _, saved := range s.savedMembers {
		m, err := host.ResumeSandboxMember(saved, s.m)
		if err != nil {
			s.log.Warn("member command refused", "session", saved.ID, "err", err)
			if s.automatic != nil || savedMemberHasStore(saved) {
				return err
			}
			continue
		}
		child := s.memberSupervisor(m)
		args := proto.CtlStartArgs{RunToken: child.runToken, Session: child.id, Agent: m.Agent.Kind, Argv: slices.Clone(m.Agent.Argv), Env: slices.Clone(m.Env), Cwd: s.m.Cwd, Cols: m.Term.Cols, Rows: m.Term.Rows, Hooks: slices.Clone(m.Agent.Hooks), Restored: true}
		s.bootDecoyArgs(&args)
		if err := s.bootPackageSettingsArgs(&args); err != nil {
			return err
		}
		shellStartArgs(m, &args)
		environment, err := s.restoredSecretEnvironment(ctx, child, secretValues(values[child.id]))
		if err != nil {
			return err
		}
		args.Secrets, args.SecretModes, args.Missing = environment.Env, environment.Modes, environment.Missing
		if err = child.installPublicSecretView(nil); err != nil {
			return err
		}
		s.memberBootPlans = append(s.memberBootPlans, memberBootPlan{child: child, args: args})
	}
	return nil
}
func savedMemberHasStore(m *host.Manifest) bool {
	agent, known := agents.Find(m.Agent.Kind)
	if !known || m.SecretState == nil {
		return false
	}
	for _, binding := range agent.SecretBindings() {
		if len(binding.Store) > 0 && m.SecretState.Intent.Modes[binding.Env] == api.SecretModeBound {
			return true
		}
	}
	return false
}

// restoreSavedMembers consumes the prevalidated selected plans without a
// second sandbox, re-read, Store allocation or namespace installation.
func (s *Supervisor) restoreSavedMembers(ctx context.Context, _ map[string]map[string]string) error {
	if len(s.savedMembers) == 0 {
		return nil
	}
	type readyMember struct {
		plan   memberBootPlan
		result proto.CtlStartResult
	}
	var ready []readyMember
	for _, plan := range s.memberBootPlans {
		child, args := plan.child, plan.args
		if err := child.listen(); err != nil {
			return err
		}
		s.membersMu.Lock()
		if s.members == nil {
			s.members = make(map[string]*Supervisor)
		}
		s.members[child.id] = child
		s.membersMu.Unlock()
		child.wg.Add(1)
		go child.acceptLoop()
		err := s.prepareMemberPhases(s.bootBudget, &args, proto.CtlSpawn, false)
		var result proto.CtlStartResult
		if err == nil {
			child.registerLaunch()
			err = s.ctl.request(ctx, proto.CtlSpawn, args, &result)
		}
		if err == nil && !s.guestABI(result.GuestABI) {
			err = execRefusal("the restored guest PTY ownership schema does not match")
		}
		if err != nil {
			s.failMember()
			child.closeSocket()
			s.removeMember(child.id)
			child.mu.Lock()
			child.closeRecord(host.EndBoot, "the member failed to restart")
			snapshot := child.snapshot()
			child.mu.Unlock()
			if saveErr := child.save(&snapshot, true); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			if s.automatic != nil {
				return err
			}
			continue
		}
		s.terminalABI = result.GuestABI
		ready = append(ready, readyMember{plan: plan, result: result})
	}
	done, cancel := phaseclock.WithDeadline(ctx, now().Add(time.Second))
	defer cancel()
	if err := s.ctl.request(done, proto.CtlMembersReady, struct{}{}, nil); err != nil {
		return err
	}
	for _, member := range ready {
		if err := member.plan.child.started(member.result, member.plan.args.Missing); err != nil {
			return err
		}
	}
	return nil
}

func secretValues(input map[string]string) map[string]api.Secret {
	if input == nil {
		return nil
	}
	out := make(map[string]api.Secret, len(input))
	for key, value := range input {
		out[key] = api.Secret(value)
	}
	return out
}
