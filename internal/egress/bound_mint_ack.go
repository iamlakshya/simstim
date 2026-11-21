package egress

func (s *Server) acknowledgeMintedState(raw []byte) error {
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.policyMu.Unlock()
	var args SecretSavedArgs
	if err := decodeBoundControlArgs(raw, &args); err != nil {
		return err
	}
	state := s.currentMembers()
	if state == nil || state.version != MemberRegistryV2 || args.Version != MemberRegistryV2 || args.Receipt == "" || args.Generation == "" {
		return secretSaveFailure()
	}
	owner, present := state.secrets[args.Owner]
	if !present || owner.bound == nil {
		return secretSaveFailure()
	}
	for index, mint := range owner.minted {
		p := mint.publication
		if mint.identity != args.Identity || p == nil || p.receipt != args.Receipt || p.generation != args.Generation || p.owner != args.Owner || p.identity != args.Identity || mint.retired {
			continue
		}
		if finished, durable := p.result(); finished {
			if durable == args.Durable {
				return nil
			}
			return secretSaveFailure()
		}
		if !args.Durable {
			p.finish(false)
			return nil
		}
		if !p.liveWaiter(state) || ctx.Err() != nil {
			p.finish(false)
			return secretSaveFailure()
		}
		next, err := cloneMemberState(state)
		if err != nil {
			p.finish(false)
			return err
		}
		updated := next.secrets[args.Owner]
		updated.minted[index].ready = true
		next.secrets[args.Owner] = updated
		prepared, err := prepareBoundRegistry(ctx, next)
		if err != nil {
			p.finish(false)
			return err
		}
		prepared.secretOnly = true
		if !p.liveWaiter(state) || ctx.Err() != nil {
			p.finish(false)
			return secretSaveFailure()
		}
		if err := s.prepareMemoryCarryLocked(ctx, prepared); err != nil {
			p.finish(false)
			return err
		}
		if err := s.retireJudgeSourcesLocked(ctx, next); err != nil {
			p.finish(false)
			return err
		}
		s.publishControlState(prepared, false)
		p.finish(true)
		return nil
	}
	return secretSaveFailure()
}
