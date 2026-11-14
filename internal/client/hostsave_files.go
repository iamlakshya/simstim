package client

import "context"

// changeHostFiles publishes a host's pin and tier alongside its saved entry.
// Tier hooks keep their lock until the hosts file commits or rolls back.
func changeHostFiles(ctx context.Context, c HostChange, hosts []Host, withPin bool, st *stage) ([]Host, hostFileHooks, error) {
	out, err := ApplyHostChange(hosts, c)
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
		removeWrappers, err := prepareHostWrapperRemoval(ctx, c.name(), out)
		if err != nil {
			return nil, hooks, err
		}
		hooks.beforeSave = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := retireMaster(context.WithoutCancel(ctx), *c.Was); err != nil {
				return err
			}
			if err := removeWrappers(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return forgetPin(c.name())
		}
	}
	return out, hooks, nil
}
