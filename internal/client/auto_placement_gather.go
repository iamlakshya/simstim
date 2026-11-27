package client

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

type autoPlacementGatherResultV1 struct {
	candidate *autoPlacementCandidate
	dial      *ownedNewDialV1
	reason    string
	err       error
}

func (p *AutoPlacementPlanV1) gatherV1(ctx context.Context) error {
	fanout, cancel := context.WithTimeout(ctx, autoPlacementFactsTimeoutV1)
	defer cancel()
	fanout = WithoutPolicyReplay(WithoutInput(WithoutInstall(WithoutWake(fanout))))
	results := make(chan autoPlacementGatherResultV1, len(p.candidates))
	for _, candidate := range p.candidates {
		go func() { results <- p.gatherOneV1(fanout, candidate) }()
	}
	var gathered []*autoPlacementCandidate
	var failures []error
	// Every spawned contact returns its actual owner, even when its hello,
	// facts read or cancellation failed. No late channel result is discarded.
	for range len(p.candidates) {
		result := <-results
		if result.dial != nil {
			p.dials = append(p.dials, result.dial)
		}
		if result.err != nil {
			failures = append(failures, result.err)
		}
		if result.reason != "" {
			p.rows = append(p.rows, AutoPlacementRowV1{Host: result.candidate.host.Name, Verdict: "skipped", Image: "-", Reason: result.reason})
		} else if result.err == nil {
			gathered = append(gathered, result.candidate)
		}
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), autoPlacementFactsTimeoutV1)
	defer stop()
	for _, owner := range p.dials {
		settled, err := owner.settle(cleanup, nil)
		if !settled && err == nil {
			err = hostAttemptUncertainV1()
		}
		if !settled || err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inflight, recent, err := p.selectionHistoryV1(ctx, gathered)
	if err != nil {
		return err
	}
	var eligible []*autoPlacementCandidate
	var observations []*hostFactsObservation
	for _, candidate := range gathered {
		reason, filterErr := p.filterCandidateV1(ctx, candidate, inflight[candidate.host.Name])
		err = filterErr
		if candidate.observation != nil {
			observations = append(observations, candidate.observation)
		}
		if err != nil {
			return err
		}
		if reason != "" {
			p.rows = append(p.rows, AutoPlacementRowV1{Host: candidate.host.Name, Verdict: "skipped", Image: "-", Reason: reason})
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(observations) != 0 {
		if err := p.store.rememberFacts(ctx, observations, p.hosts); err != nil {
			return err
		}
	}
	rankPlacementCandidates(eligible, p.settings, recent)
	if len(eligible) != 0 {
		p.summary = autoPlacementDisplaySummaryV1(eligible[0])
	}
	for index, candidate := range eligible {
		candidate.reason = explainAutoPlacementV1(candidate, p.settings)
		p.rows = append(p.rows, autoPlacementDisplayRowV1(candidate, index))
	}
	if err := p.selection.Check(ctx); err != nil {
		return err
	}
	p.candidates, p.prepared = eligible, true
	return nil
}

func (p *AutoPlacementPlanV1) gatherOneV1(ctx context.Context, candidate *autoPlacementCandidate) (result autoPlacementGatherResultV1) {
	result.candidate = candidate
	if err := p.checkCandidateSourceV1(ctx, candidate); err != nil {
		result.err = err
		return result
	}
	cached, err := p.store.cachedFacts(ctx, candidate.host)
	if err != nil {
		result.err = err
		return result
	}
	state, err := placementMachineStateV1(ctx, candidate.host)
	if sourceErr := p.checkCandidateSourceV1(ctx, candidate); sourceErr != nil {
		result.err = sourceErr
		return result
	}
	if err != nil {
		result.reason = "away: provider status is unavailable"
		return result
	}
	if state == MachineMissing {
		result.reason = "the provider machine is missing"
		return result
	}
	if state == MachineStopped || state == MachineStarting {
		if cached == nil {
			result.reason = "stopped, with no original-source cached facts"
			return result
		}
		candidate.cached, candidate.stopped = cached, true
		candidate.wakeClass = placementWakeClassV1(candidate.host)
		if candidate.wakeClass == 0 {
			result.reason = "away, with no wake"
		}
		return result
	}
	if cached != nil {
		age := time.Since(cached.entry.ReceivedAt)
		freshNeeded, err := p.store.cachedFactsNeedCompletionV1(ctx, cached)
		if err != nil {
			result.err = err
			return result
		}
		if age >= 0 && age < 30*time.Second && !freshNeeded {
			candidate.cached = cached
			return result
		}
	}
	result.dial, err = dialOwnedNewAttemptV1(ctx, candidate.host, false)
	if err != nil {
		if sourceErr := p.checkCandidateSourceV1(context.WithoutCancel(ctx), candidate); sourceErr != nil {
			result.err = sourceErr
			return result
		}
		result.reason = "away: no current facts within the fan-out deadline"
		return result
	}
	conn := result.dial.connection()
	if conn == nil {
		result.err = grantOperationError()
		return result
	}
	candidate.observation, candidate.placementFence, err = p.store.readFencedHostFactsV1(ctx, conn)
	if err != nil {
		if sourceErr := p.checkCandidateSourceV1(context.WithoutCancel(ctx), candidate); sourceErr != nil {
			result.err = sourceErr
			return result
		}
		result.reason = "away: current facts are unavailable"
		return result
	}
	if !sameHostTransportSource(candidate.originalSource, candidate.observation.source) {
		result.err = grantIdentityError(candidate.host)
	}
	return result
}

func (p *AutoPlacementPlanV1) checkCandidateSourceV1(ctx context.Context, candidate *autoPlacementCandidate) error {
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	source, err := captureHostSelectionSource(ctx, candidate.host)
	if err != nil {
		return err
	}
	if !sameHostTransportSource(source, candidate.originalSource) {
		return grantIdentityError(candidate.host)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	if tiers.Of(candidate.host.Name) != candidate.tier || tiers.IDChanged(candidate.host.Name) {
		return grantIdentityError(candidate.host)
	}
	return ctx.Err()
}

// Status is queried only after the caller completed every local filter. Its
// answer changes wake metadata, never the accepted host identity or grants.
func placementMachineStateV1(ctx context.Context, host Host) (string, error) {
	if host.Machine != nil {
		machine, err := OpenMachine(host)
		if err != nil {
			return "", err
		}
		return machine.Status(ctx)
	}
	if host.Provider != nil && host.Provider.WakeOnAttach {
		raw, err := RunProvider(ctx, host, "status")
		if err != nil {
			return "", err
		}
		var status ProviderStatus
		if json.Unmarshal(raw, &status) != nil || status.State != MachineRunning && status.State != MachineStopped {
			return "", placementConfigError()
		}
		return status.State, nil
	}
	return MachineRunning, nil
}

func placementWakeClassV1(host Host) int {
	cost := host.Place.Cost
	if host.Machine != nil {
		if cost == "" {
			if host.Machine.Provider == "lima" || host.Machine.Provider == "docker" {
				cost = "free"
			} else {
				cost = "paid"
			}
		}
	} else if host.Provider != nil && host.Provider.WakeOnAttach {
		if cost == "" {
			cost = host.Provider.Cost
		}
		if cost == "" {
			cost = "paid"
		}
	} else {
		return 0
	}
	if cost == "free" {
		return 1
	}
	return 2
}

func (p *AutoPlacementPlanV1) selectionHistoryV1(ctx context.Context, candidates []*autoPlacementCandidate) (map[string]int64, []string, error) {
	inflight := make(map[string]int64, len(candidates))
	var recent []string
	err := p.store.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := p.store.readPlacements(owned)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, candidate := range candidates {
			facts := candidate.selectionFactsV1()
			amount, err := placementResidualV1(state.Records, candidate.host.Name, facts.HostID, candidate.originalSource.generation, facts, candidate.observation, candidate.placementFence, now)
			if err != nil {
				return err
			}
			inflight[candidate.host.Name] = amount
		}
		for _, record := range state.Records {
			if age := now.Sub(record.At); age >= 0 && age < 10*time.Minute {
				recent = append(recent, record.Host)
			}
		}
		return nil
	})
	return inflight, recent, err
}

func autoPlacementWakeNameV1(wakeClass int) string {
	return []string{"awake", "free wake", "paid wake"}[min(max(wakeClass, 0), 2)]
}

func explainAutoPlacementV1(candidate *autoPlacementCandidate, settings PlacementSettings) string {
	parts := []string{autoPlacementWakeNameV1(candidate.wakeClass)}
	if candidate.penalty > 0 {
		parts = append(parts, "pressure or recent placement penalty")
	}
	if slices.Contains(settings.Prefer, candidate.host.Name) {
		parts = append(parts, "preferred host")
	}
	if candidate.imageCached {
		parts = append(parts, "image cached")
	} else if candidate.need.ImageDigest != "" {
		parts = append(parts, "image missing")
	}
	if candidate.near {
		parts = append(parts, "near")
	}
	if candidate.pendingGrants {
		parts = append(parts, "credential confirmation required")
	}
	if settings.Strategy == "pack" {
		parts = append(parts, "least room that fits")
	} else {
		parts = append(parts, "most room")
	}
	return strings.Join(parts, ", ")
}
