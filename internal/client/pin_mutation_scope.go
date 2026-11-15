package client

import (
	"context"
	"sync"
)

// A saved pin changes under the same exclusive lifetime as its tier record.
// Unsaved explicit/staged pin operations retain their existing stage contract;
// this helper never fabricates saved membership or promotes that stage.
func holdSavedPinMutation(ctx context.Context, h Host, kind string) (context.Context, func(), error) {
	// A provider's actual SSH route is not its saved machine identity.
	// Only the private original route context may supply this expectation.
	if origin, _ := ctx.Value(sshOriginKey{}).(*Host); origin != nil {
		if origin.Name != h.Name {
			return ctx, nil, grantIdentityError(h)
		}
		h = *origin
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	saved, err := SavedEntry(h.Name)
	if err != nil {
		return ctx, nil, err
	}
	if saved == nil {
		return ctx, func() {}, nil
	}
	if !SameEntry(*saved, h) {
		return ctx, nil, grantIdentityError(h)
	}
	owned, endOP, err := LockHost(ctx, h.Name, kind)
	if err != nil {
		return ctx, nil, err
	}
	owned, endMutation, err := holdHostMutationContext(owned, h.Name)
	if err != nil {
		endOP()
		return ctx, nil, err
	}
	release := sync.OnceFunc(func() { endMutation(); endOP() })
	err = withHostMembershipContext(owned, func(member context.Context) error {
		if err := checkTierMutationSource(member, h); err != nil {
			return err
		}
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		_, err = ApplyHostChange(hosts, HostChange{Kind: kind, Was: &h, Now: &h})
		return err
	})
	if err != nil {
		release()
		return ctx, nil, err
	}
	return owned, release, nil
}
