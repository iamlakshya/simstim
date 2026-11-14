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
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
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
	return writeSavedTierFile(ts)
}

// writeSavedTierFile lets transaction tests fail only the final write.
var writeSavedTierFile = writeTiersFile
