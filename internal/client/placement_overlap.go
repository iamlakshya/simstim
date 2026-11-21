package client

import (
	"context"
	"math"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The fence is captured from the existing local store BEFORE the actual
// facts RPC. Only that real reply can bind it. No timestamp or cached DTO
// can manufacture this causal ordering for a completed local start.
type placementCompletionFenceV1 struct {
	store      *placementStore
	host       string
	hostID     string
	generation string
	source     hostStateSource
	completed  map[string]placementRecordV1
	observed   *hostFactsObservation
}

func (s *placementStore) readFencedHostFactsV1(ctx context.Context, conn *Conn) (*hostFactsObservation, *placementCompletionFenceV1, error) {
	if s == nil || conn == nil {
		return nil, nil, grantOperationError()
	}
	if err := checkFactsConnection(ctx, conn); err != nil {
		return nil, nil, err
	}
	fence := &placementCompletionFenceV1{store: s, host: conn.Host.Name, hostID: conn.HostID(),
		generation: conn.hostTransport.source.generation, source: conn.hostTransport.source, completed: map[string]placementRecordV1{}}
	if err := s.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := s.readPlacements(owned)
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.Host == fence.host && record.HostID == fence.hostID && record.Generation == fence.generation && record.Session != "" && !record.Pending {
				fence.completed[record.ID] = record
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	observed, err := conn.readHostFacts(ctx)
	if err != nil {
		return nil, nil, err
	}
	if observed.host.Name != fence.host || observed.hostID != fence.hostID || !sameHostTransportSource(observed.source, fence.source) {
		return nil, nil, grantIdentityError(conn.Host)
	}
	fence.observed = observed
	return observed, fence, nil
}

func placementObservedChargesV1(facts api.HostFacts) (map[string]int64, bool) {
	observation := facts.MemoryObservation
	if observation == nil || observation.Version != api.MemoryObservationVersionV1 || !observation.Complete ||
		!facts.MemoryKnown || !facts.AccountingKnown || validateHostFacts(facts, facts.HostID) != nil || api.ValidateMemoryObservationV1(observation, &facts) != nil {
		return nil, false
	}
	// The shared facts validator binds uniqueness, aggregate and sample
	// identity. These values remain advisory, never admission authority.
	charges := make(map[string]int64, len(observation.Charges))
	for _, charge := range observation.Charges {
		charges[charge.PrimaryID] = charge.Charged
	}
	return charges, true
}

func placementResidualV1(records []placementRecordV1, host, hostID, generation string, facts api.HostFacts, observed *hostFactsObservation, fence *placementCompletionFenceV1, now time.Time) (int64, error) {
	charges, complete := placementObservedChargesV1(facts)
	causal := complete && fence != nil && observed != nil && fence.observed == observed &&
		fence.host == host && fence.hostID == hostID && fence.generation == generation && fence.store != nil && sameHostTransportSource(fence.source, observed.source)
	if causal {
		for _, record := range records {
			if before, ok := fence.completed[record.ID]; ok && record.Session != "" && !record.Pending && samePlacementRecordV1(before, record) {
				charges[record.Session] = 0
			}
		}
	}
	var total int64
	for _, record := range records {
		if record.Host != host || record.HostID != hostID || record.Generation != generation {
			continue
		}
		// A future wall-clock value cannot create room. Only the established
		// positive age expiry removes an old local inflight requirement.
		if age := now.Sub(record.At); age >= time.Minute {
			continue
		}
		if record.Memory <= 0 {
			return 0, placementStoreError(errPlacementState)
		}
		if causal && record.Session != "" && !record.Pending {
			if before, ok := fence.completed[record.ID]; ok && samePlacementRecordV1(before, record) {
				continue
			}
		}
		residual := record.Memory
		if complete && record.Session != "" {
			included := min(residual, charges[record.Session])
			residual -= included
			charges[record.Session] -= included // one native charge cannot cover two pending records
		}
		if total > math.MaxInt64-residual {
			return 0, placementStoreError(errPlacementState)
		}
		total += residual
	}
	return total, nil
}

func samePlacementRecordV1(a, b placementRecordV1) bool {
	return a.ID == b.ID && a.Host == b.Host && a.HostID == b.HostID && a.Generation == b.Generation &&
		a.Memory == b.Memory && a.Session == b.Session && a.Pending == b.Pending && a.Reason == b.Reason && a.At.Equal(b.At)
}

// Cached facts without a causal completed snapshot may need one fresh RPC.
// This avoids a sixty-second phantom charge after a completed start ended.
func (s *placementStore) cachedFactsNeedCompletionV1(ctx context.Context, cached *cachedPlacementFacts) (bool, error) {
	if cached == nil {
		return false, nil
	}
	if err := cached.check(ctx); err != nil {
		return false, err
	}
	charges, complete := placementObservedChargesV1(cached.entry.Facts)
	needed := false
	err := s.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := s.readPlacements(owned)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, record := range state.Records {
			if record.Host == cached.host.Name && record.HostID == cached.entry.HostID && record.Generation == cached.entry.Generation &&
				now.Sub(record.At) < time.Minute && record.Session != "" && !record.Pending && (!complete || charges[record.Session] < record.Memory) {
				needed = true
				break
			}
		}
		return nil
	})
	return needed, err
}
