package client

import "context"

// mutateSavedHostTiers checks the observed host while holding the hosts
// file lock, then saves its tier under the final lock in the order. The
// It takes or reuses HostLock. Callers migrate legacy pins before any pin lock.
// Neither change nor a tier writer may acquire an earlier lock.
func mutateSavedHostTiers(ctx context.Context, expected Host, kind string, change func(*Tiers) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, unlockHost, err := LockHost(ctx, expected.Name, kind)
	if err != nil {
		return err
	}
	defer unlockHost()
	ctx, endMutation, err := holdHostMutationContext(ctx, expected.Name)
	if err != nil {
		return err
	}
	defer endMutation()
	ctx, unlockHosts, err := acquireHostMembershipContext(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
	if err := checkTierMutationSource(ctx, expected); err != nil {
		return err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	if _, err := ApplyHostChange(hosts, HostChange{Kind: kind, Was: &expected, Now: &expected}); err != nil {
		return err
	}
	unlockTiers, err := lockTiersFile(ctx)
	if err != nil {
		return err
	}
	defer unlockTiers()
	ts, err := readTiersFile()
	if err != nil {
		return err
	}
	if err := change(&ts); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkTierMutationSource(ctx, expected); err != nil {
		return err
	}
	return writeSavedTierFile(ts)
}

// writeSavedTierFile lets transaction tests fail only the final write.
var writeSavedTierFile = writeTiersFile

// The OP's original native Source is already frozen by the canonical locker.
// A carried observation is stricter and must never be replaced by a fresh one.
func checkTierMutationSource(ctx context.Context, expected Host) error {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	held, _ := ctx.Value(heldKey{expected.Name}).(*heldLock)
	if held == nil {
		return hostStateRemovalError("tier mutation has no original host operation")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	if observed, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observed != nil {
		if observed.name != expected.Name || !SameEntry(observed.prospective, expected) {
			return hostStateRemovalError("tier mutation belongs to another observed host")
		}
		if (observed.role == hostStateSaved || observed.role == hostStateRead) && observed.op != held {
			return hostStateRemovalError("tier mutation changed its observed physical operation")
		}
		if observed.role == hostStateRegistration {
			if captured := observed.capturedOP.Load(); captured != nil && captured != held {
				return hostStateRemovalError("tier mutation changed its captured registration operation")
			}
		}
		return observed.checkMembership(ctx)
	}
	return held.native.source.check(ctx)
}
