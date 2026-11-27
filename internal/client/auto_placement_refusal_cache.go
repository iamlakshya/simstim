package client

import "context"

// A correlated capacity refusal refreshes only cached room metadata. It never
// becomes a hostFactsObservation, changes a native grant or renews the age of
// unrelated cached facts. Unknown stored formats are retained.
func (p *AutoPlacementPlanV1) rememberSettledNoRoomV1(ctx context.Context, attempt *HostAttemptV1, proof *settledNewRefusalV1) error {
	if p == nil || p.store == nil || attempt == nil || proof == nil || attempt.candidate == nil ||
		proof.owner == nil || proof.owner.attempt != attempt {
		return grantOperationError()
	}
	if err := proof.checkOriginalV1(ctx); err != nil {
		return err
	}
	candidate := attempt.candidate
	if candidate.observation == nil || !SameEntry(candidate.host, proof.owner.conn.Host) ||
		candidate.observation.hostID != proof.settlement.HostID {
		return grantOperationError()
	}
	facts := proof.room
	if !facts.Known || facts.Budget <= 0 || facts.Needed <= facts.Room || facts.Room != facts.Budget-facts.Reserved ||
		facts.At.IsZero() || facts.BootID == "" {
		return nil
	}
	if err := p.checkCandidateSourceV1(ctx, candidate); err != nil {
		return err
	}
	return p.store.locked(ctx, "facts.json", func(owned context.Context) error {
		cache := placementFactsCacheV1{Version: 1, Hosts: map[string]placementFactsV1{}}
		raw, exists, err := p.store.read(owned, "facts.json")
		if err != nil {
			return err
		}
		if exists {
			cache, err = decodePlacementFactsCache(raw)
			if err != nil {
				return err
			}
		}
		entry, found := cache.Hosts[candidate.host.Name]
		if found && (entry.HostID != proof.settlement.HostID || entry.Generation != candidate.originalSource.generation) {
			return grantIdentityError(candidate.host)
		}
		if found && entry.ReceivedAt.After(proof.receivedAt) {
			return nil
		}
		if !found {
			observed := candidate.observation
			entry = placementFactsV1{HostID: observed.hostID, Generation: observed.source.generation,
				ReceivedAt: observed.receivedAt, RoundTrip: observed.roundTrip, Facts: placementCacheFacts(observed.facts)}
		}
		if updated, coherent := updateCachedPlacementRoomV1(entry, facts); coherent {
			cache.Hosts[candidate.host.Name] = updated
		} else {
			delete(cache.Hosts, candidate.host.Name)
		}
		return p.store.write(owned, "facts.json", cache)
	})
}

func updateCachedPlacementRoomV1(entry placementFactsV1, facts newNoRoomDiagnosticsV1) (placementFactsV1, bool) {
	if entry.Facts.BootID != facts.BootID {
		return entry, false
	}
	// A partial refusal has no complete same-scan root list. Never retain
	// the older list while replacing its aggregate room underneath it.
	entry.Facts.MemoryObservation = nil
	entry.Facts.MemForSessions, entry.Facts.MemReserved, entry.Facts.Room = facts.Budget, facts.Reserved, facts.Room
	entry.Facts.AccountingKnown, entry.Facts.Sandboxes = true, facts.Sandboxes
	return entry, validateHostFacts(entry.Facts, entry.HostID) == nil
}
