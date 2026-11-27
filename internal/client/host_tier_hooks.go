package client

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// applyHostChangeTier takes the final lock in the host save order. Its
// release hook runs after commit or rollback and before hosts.toml unlocks.
func applyHostChangeTier(ctx context.Context, hosts []Host, c HostChange) (hostFileHooks, error) {
	name := c.name()
	if !ValidHostName(name) {
		return hostFileHooks{}, nil
	}
	var pinnedKey string
	if pin, err := PinOf(name); err == nil {
		pinnedKey = pin.Fingerprint
	}
	if c.Now == nil && pinnedKey == "" {
		if _, err := os.Lstat(tiersPath()); errors.Is(err, os.ErrNotExist) {
			return hostFileHooks{}, nil
		} else if err != nil {
			return hostFileHooks{}, fmt.Errorf("stat tiers file before forgetting a host: %w", err)
		}
	}
	unlock, err := lockTiersFile(ctx)
	if err != nil {
		return hostFileHooks{}, err
	}
	hooks := hostFileHooks{release: unlock}
	ts, err := readTiersFile()
	missing := errors.Is(err, os.ErrNotExist)
	if missing {
		ts = buildMigratedTiers(hosts)
	} else if err != nil {
		return hooks, err
	}
	if c.Now == nil {
		return prepareTierForget(ctx, hooks, ts, name, pinnedKey, missing)
	}
	prev := Tiers{Migrated: ts.Migrated, entries: canonicalTierEntries(ts)}
	applySavedTier(&ts, c, pinnedKey)
	if err := ctx.Err(); err != nil {
		return hooks, err
	}
	if err := writeHostTierFile(ts); err != nil {
		return hooks, err
	}
	hooks.rollback = func() {
		if err := writeHostTierFile(prev); err != nil {
			DebugLog().Warn("could not restore tiers after a failed host save", "host", name, "err", err)
		}
	}
	return hooks, nil
}

// prepareTierForget keeps the key guard until the hosts file commits. A
// failed cleanup may leave a row, which a later add must treat as orphaned.
func prepareTierForget(ctx context.Context, hooks hostFileHooks, ts Tiers, name, pinnedKey string, missing bool) (hostFileHooks, error) {
	e, had := ts.entries[name]
	if pinnedKey != "" && (missing || e.Key == "") {
		if !had {
			e = TierEntry{Name: name, Tier: TierShared, SetAt: nowUTC()}
		}
		e.Key = pinnedKey
		ts.entries[name] = e
		if err := ctx.Err(); err != nil {
			return hooks, err
		}
		if err := writeHostTierFile(ts); err != nil {
			return hooks, err
		}
	}
	hooks.commit = func() {
		delete(ts.entries, name)
		if err := writeHostTierFile(ts); err != nil {
			DebugLog().Warn("could not remove the tier of a forgotten host", "host", name, "err", err)
		}
	}
	return hooks, nil
}

// applySavedTier never lends an orphan row to a newly added host.
func applySavedTier(ts *Tiers, c HostChange, pinnedKey string) {
	name := c.name()
	e, had := ts.entries[name]
	if c.Was == nil {
		e, had = TierEntry{Name: name}, false
	}
	e.Name = name
	if c.Tier.Valid() {
		e.Tier = c.Tier
		e.NeedsAnswer = false
		e.IDChanged = false
		e.SetAt = nowUTC()
	} else if !had {
		e.Tier = TierShared
		e.NeedsAnswer = true
		e.SetAt = nowUTC()
	}
	if c.HostID != "" && ValidHostID(c.HostID) {
		e.HostID = c.HostID
	}
	if pinnedKey != "" {
		e.Key = pinnedKey
	}
	ts.entries[name] = e
}

// writeHostTierFile is replaced by transaction tests to fail a guarded
// write or post-commit cleanup without changing the ordinary tier writer.
var writeHostTierFile = writeTiersFile
