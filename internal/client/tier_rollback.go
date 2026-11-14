package client

import "context"

// canonicalTierEntries keeps the rows that writeTiersFile writes, under the
// exact names that readTiersFile uses. Time comparison uses Equal after TOML.
func canonicalTierEntries(ts Tiers) map[string]TierEntry {
	entries := make(map[string]TierEntry, len(ts.entries))
	for _, e := range ts.entries {
		if ValidHostName(e.Name) && e.Tier.Valid() {
			entries[e.Name] = e
		}
	}
	return entries
}

func sameTierEntry(a, b TierEntry) bool {
	return a.Name == b.Name && a.Tier == b.Tier && a.HostID == b.HostID &&
		a.Key == b.Key && a.SetAt.Equal(b.SetAt) && a.IDChanged == b.IDChanged && a.NeedsAnswer == b.NeedsAnswer
}

type tierUndo struct {
	name                string
	before, after       TierEntry
	beforeHas, afterHas bool
}

// tierRollback reverses only this transaction's unchanged rows. A later
// writer's rows and the current migration metadata always stay in place.
func tierRollback(ctx context.Context, before, after map[string]TierEntry) func() {
	var changes []tierUndo
	seen := make(map[string]bool, len(before)+len(after))
	for _, entries := range []map[string]TierEntry{before, after} {
		for name := range entries {
			if seen[name] {
				continue
			}
			seen[name] = true
			a, ah := before[name]
			b, bh := after[name]
			if ah != bh || !sameTierEntry(a, b) {
				changes = append(changes, tierUndo{name: name, before: a, after: b, beforeHas: ah, afterHas: bh})
			}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return func() { restoreTierEntries(context.WithoutCancel(ctx), changes) }
}

func restoreTierEntries(ctx context.Context, changes []tierUndo) {
	unlock, err := lockTiersFile(ctx)
	if err != nil {
		DebugLog().Warn("could not lock tiers file for rollback", "err", err)
		return
	}
	defer unlock()
	cur, err := readTiersFile()
	if err != nil {
		DebugLog().Warn("could not read tiers file for rollback", "err", err)
		return
	}
	changed := false
	for _, undo := range changes {
		live, has := cur.entries[undo.name]
		if has != undo.afterHas || has && !sameTierEntry(live, undo.after) {
			DebugLog().Warn("kept a newer host tier during rollback", "host", undo.name)
			continue
		}
		if undo.beforeHas {
			cur.entries[undo.name] = undo.before
		} else {
			delete(cur.entries, undo.name)
		}
		changed = true
	}
	if changed {
		if err := writeTiersFile(cur); err != nil {
			DebugLog().Warn("could not restore tiers file during rollback", "err", err)
		}
	}
}
