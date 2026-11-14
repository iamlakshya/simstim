package client

import "context"

// changeHostFiles publishes a host's pin and tier alongside its saved entry.
// Tier hooks keep their lock until the hosts file commits or rolls back.
func changeHostFiles(ctx context.Context, c HostChange, hosts []Host, withPin bool, st *stage) ([]Host, hostFileHooks, error) {
	out, err := ApplyHostChange(hosts, c)
	if err != nil {
		return nil, hostFileHooks{}, err
	}
	sourceGeneration, generation, err := prepareHostForgetGeneration(ctx, c, hosts)
	if err != nil {
		return nil, hostFileHooks{}, err
	}
	var pinRollback func()
	if withPin {
		publish, err := checkStagedPin(ctx, *c.Now, hosts, st)
		if err != nil {
			return nil, hostFileHooks{}, err
		}
		if pinRollback, err = publish(); err != nil {
			return nil, hostFileHooks{}, err
		}
	}
	hooks, err := applyHostChangeTier(ctx, hosts, c)
	if err != nil {
		if pinRollback != nil {
			pinRollback()
		}
		return nil, hooks, err
	}
	if pinRollback != nil {
		tierRollback := hooks.rollback
		hooks.rollback = func() {
			if tierRollback != nil {
				tierRollback()
			}
			pinRollback()
		}
	}
	if c.Now == nil && ValidHostName(c.name()) {
		removeWrappers, wrapperErr := prepareHostWrapperRemoval(ctx, c.name(), out)
		hooks.beforeSave = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Cleanup starts after the final cancellation gate. Retire the
			// pin first: a failed socket or wrapper cleanup keeps a retryable
			// saved entry, but cannot leave it connectable under its old key.
			if err := forgetPin(c.name()); err != nil {
				return err
			}
			if err := retireMaster(context.WithoutCancel(ctx), *c.Was); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if wrapperErr != nil {
				return wrapperErr
			}
			return removeWrappers()
		}
	}
	hooks.membershipGeneration = generation
	hooks.membershipSourceGeneration = sourceGeneration
	priorCommit := hooks.commit
	hooks.commit = func() {
		if priorCommit != nil {
			priorCommit()
		}
		recordHostForgetWitness(ctx, c, generation)
	}
	return out, hooks, nil
}
