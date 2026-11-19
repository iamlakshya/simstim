package client

import (
	"cmp"
	"slices"
)

// rankPlacementCandidates uses the documented tuple, never a weighted score.
// Pressure/battery and recent imbalance penalties come directly after wake.
func rankPlacementCandidates(candidates []*autoPlacementCandidate, settings PlacementSettings, recent []string) {
	for _, candidate := range candidates {
		candidate.penalty = placementPressurePenalty(candidate.observation.facts)
		candidate.prefer = len(settings.Prefer)
		if at := slices.Index(settings.Prefer, candidate.host.Name); at >= 0 {
			candidate.prefer = at
		}
		if len(candidates) > 1 && len(recent) >= 4 {
			count := 0
			for _, host := range recent {
				if host == candidate.host.Name {
					count++
				}
			}
			if count > len(recent)/2 {
				candidate.penalty++
			}
		}
	}
	slices.SortStableFunc(candidates, func(a, b *autoPlacementCandidate) int {
		for _, order := range []int{cmp.Compare(a.wakeClass, b.wakeClass), cmp.Compare(a.penalty, b.penalty), cmp.Compare(a.prefer, b.prefer)} {
			if order != 0 {
				return order
			}
		}
		for _, pair := range [][2]bool{{a.imageCached, b.imageCached}, {a.near, b.near}} {
			if pair[0] != pair[1] {
				if pair[0] {
					return -1
				}
				return 1
			}
		}
		if order := cmp.Compare(a.room, b.room); order != 0 {
			if settings.Strategy == "pack" {
				return order
			}
			return -order
		}
		if order := cmp.Compare(a.observation.facts.Sandboxes, b.observation.facts.Sandboxes); order != 0 {
			return order
		}
		return cmp.Compare(a.host.Name, b.host.Name)
	})
}
