package client

import (
	"context"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

type autoPlacementCandidate struct {
	placementFence *placementCompletionFenceV1
	selectedWall   string
	cached         *cachedPlacementFacts // advisory only; never an accepted observation
	originalSource hostStateSource
	localArgs      NewArgs
	runtimeUpgrade bool
	stopped        bool
	pendingGrants  bool
	host           Host
	tier           Tier
	observation    *hostFactsObservation
	need           PlacementNeed
	level          string
	declared       int64
	room           int64
	wakeClass      int // awake, free wake, paid wake
	penalty        int
	prefer         int
	imageCached    bool
	near           bool
	reason         string
}

// filterPlacementFacts uses only a genuine Source-bound observation. Passing
// this filter is metadata eligibility; it cannot reserve or launch a sandbox.
func filterPlacementFacts(ctx context.Context, candidate *autoPlacementCandidate, inFlight int64, stopped, runtimeUpgrade bool) (string, error) {
	if candidate == nil || candidate.observation == nil || inFlight < 0 {
		return "current host facts are unavailable", nil
	}
	if err := validatePlacementNeed(candidate.need); err != nil {
		return "", err
	}
	if err := candidate.observation.check(ctx); err != nil {
		return "", err
	}
	if !SameEntry(candidate.host, candidate.observation.host) {
		return "", grantIdentityError(candidate.host)
	}
	return filterPlacementFactsDataV1(candidate, inFlight, stopped, runtimeUpgrade, candidate.observation.facts, candidate.observation.roundTrip)
}

// Only these two checked wrappers may feed the common metadata filter. Cached
// data remains a distinct type and never becomes a hostFactsObservation.
func filterCachedPlacementFactsV1(ctx context.Context, candidate *autoPlacementCandidate, inFlight int64, stopped, runtimeUpgrade bool) (string, error) {
	if candidate == nil || candidate.cached == nil || inFlight < 0 {
		return "cached host facts are unavailable", nil
	}
	if err := validatePlacementNeed(candidate.need); err != nil {
		return "", err
	}
	if err := candidate.cached.check(ctx); err != nil {
		return "", err
	}
	if !SameEntry(candidate.host, candidate.cached.host) {
		return "", grantIdentityError(candidate.host)
	}
	return filterPlacementFactsDataV1(candidate, inFlight, stopped, runtimeUpgrade, candidate.cached.entry.Facts, candidate.cached.entry.RoundTrip)
}

func filterPlacementFactsDataV1(candidate *autoPlacementCandidate, inFlight int64, stopped, runtimeUpgrade bool, facts api.HostFacts, roundTrip time.Duration) (string, error) {
	need := candidate.need
	if facts.OS != "linux" || need.Arch != "" && facts.Arch != need.Arch {
		return "the host platform does not match", nil
	}
	if !stopped && (!facts.MemoryKnown || !facts.AccountingKnown) {
		return "native memory accounting is unavailable", nil
	}
	if !facts.DrainKnown || facts.Drain {
		return "the host is draining or its drain state is unavailable", nil
	}
	minimum := wallLevelRank(need.MinLevel)
	if candidate.host.WallFloor != "" {
		floor := wallLevelRank(candidate.host.WallFloor)
		if floor == 0 {
			return "the saved host floor is invalid", nil
		}
		minimum = max(minimum, floor)
	}
	var selected *api.WallProbe
	for i := range facts.Walls {
		probe := &facts.Walls[i]
		if facts.WallsKnown && wallLevelRank(probe.Level) >= minimum && (candidate.selectedWall == "" || probe.Level == candidate.selectedWall) && (probe.OK || probe.CanPrepare) &&
			(selected == nil || wallLevelRank(probe.Level) > wallLevelRank(selected.Level)) {
			selected = probe
		}
	}
	if selected == nil {
		return "the required wall cannot be constructed", nil
	}
	candidate.level, candidate.declared = selected.Level, need.Memory
	if selected.Level == "microvm" {
		budget, err := vmneed.BudgetFor(need.Memory)
		if err != nil {
			return "", placementNeedError()
		}
		candidate.declared = budget.Limit
		if candidate.localArgs.MemoryMax > 0 && candidate.localArgs.MemoryMax != budget.Limit {
			return "the original memory cap differs from the selected complete microvm size", nil
		}
	} else if candidate.localArgs.MemoryMax > 0 {
		if err := checkPlacementCapRepresentableV1(candidate.localArgs.MemoryMax); err != nil {
			return "", err
		}
		candidate.declared = candidate.localArgs.MemoryMax
	}
	for _, health := range facts.Health {
		if health.Severity != "blocks_new" || len(health.Levels) != 0 && !slices.Contains(health.Levels, selected.Level) {
			continue
		}
		if health.ID == "runtime-missing" && selected.CanPrepare && runtimeUpgrade {
			continue
		}
		if stopped && health.ID == "memory_accounting" {
			continue // The actual fresh post-wake filter must check room again.
		}
		return "host health blocks the selected wall: " + CleanLine(health.ID), nil
	}
	if !selected.OK && (!selected.CanPrepare || !runtimeUpgrade) {
		return "the required runtime cannot be prepared", nil
	}
	missingRuntime := len(need.RuntimeDigests) != 0 && !facts.RuntimeDigestsKnown
	for _, digest := range need.RuntimeDigests {
		missingRuntime = missingRuntime || !slices.Contains(facts.RuntimeDigests, digest)
	}
	if missingRuntime && !runtimeUpgrade {
		return "the required client runtime cannot be prepared", nil
	}
	candidate.room = max(int64(0), facts.Room)
	if !stopped {
		candidate.room = max(int64(0), facts.Room)
		if inFlight >= candidate.room {
			candidate.room = 0
		} else {
			candidate.room -= inFlight
		}
		if candidate.declared > candidate.room {
			return "the complete sandbox size does not fit the live room", nil
		}
	}
	if maximum := candidate.host.Place.MaxSandboxes; maximum > 0 && facts.Sandboxes >= maximum {
		return "the local sandbox count limit is full", nil
	}
	candidate.imageCached = need.ImageDigest != "" && facts.ImageDigestsKnown && slices.Contains(facts.ImageDigests, need.ImageDigest)
	disk := need.Disk
	if need.ImageDigest != "" && !candidate.imageCached {
		if !need.ImageBytesKnown || disk > int64(^uint64(0)>>1)-need.ImageBytes {
			return "the missing image size is unavailable", nil
		}
		disk += need.ImageBytes
	}
	if !facts.DiskKnown || facts.DiskFree < disk {
		return "the image and sandbox do not fit the observed free disk", nil
	}
	if prepared := candidate.host.Prepared; prepared != nil {
		if prepared.Arch != "" && prepared.Arch != facts.Arch || prepared.Level != "" && facts.LevelKnown && wallLevelRank(facts.Level) > wallLevelRank(prepared.Level) {
			return "live facts disagree with the saved preparation", nil
		}
		if prepared.MemTotal > 0 && facts.MemoryKnown {
			difference := facts.MemTotal - prepared.MemTotal
			if difference < 0 {
				difference = -difference
			}
			if difference > prepared.MemTotal/100 {
				return "live memory differs from preparation by more than one per cent", nil
			}
		}
	}
	candidate.near = !stopped && roundTrip <= 10*time.Millisecond
	candidate.penalty = placementPressurePenalty(facts)
	return "", nil
}

func placementPressurePenalty(facts api.HostFacts) int {
	if facts.PressureMemKnown && facts.PressureMem >= 10 || facts.PressureCPUKnown && facts.PressureCPU >= 50 || facts.OnBatteryKnown && facts.OnBattery {
		return 1
	}
	return 0
}

// These are ranking data only. Native admission and the H19 binder continue
// to require candidate.observation, never this DTO accessor.
func (candidate *autoPlacementCandidate) selectionFactsV1() api.HostFacts {
	if candidate.observation != nil {
		return candidate.observation.facts
	}
	if candidate.cached != nil {
		return candidate.cached.entry.Facts
	}
	return api.HostFacts{}
}
