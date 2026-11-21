package client

import (
	"strings"
)

// AutoPlacementSummaryV1 is bounded display metadata, never a native Need.
type AutoPlacementSummaryV1 struct {
	Available bool
	Memory    int64
	CPUs      int
	Level     string
	Image     string
}

func (p *AutoPlacementPlanV1) Summary() AutoPlacementSummaryV1 {
	if p == nil {
		return AutoPlacementSummaryV1{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.prepared {
		return AutoPlacementSummaryV1{}
	}
	return p.summary
}

func autoPlacementDisplaySummaryV1(candidate *autoPlacementCandidate) AutoPlacementSummaryV1 {
	size, err := ResolveNewVMOptions(candidate.localArgs, "auto")
	if err != nil {
		return AutoPlacementSummaryV1{}
	}
	image := ""
	if digest := strings.TrimPrefix(candidate.need.ImageDigest, "sha256:"); len(digest) >= 8 {
		image = "image@" + digest[:8]
		if size.Image == "default" {
			image = "agent-base@" + digest[:8]
		}
	}
	memory := candidate.need.Memory
	if candidate.level == "gvisor" && candidate.localArgs.MemoryMax > 0 {
		memory = candidate.localArgs.MemoryMax
	}
	return AutoPlacementSummaryV1{Available: true, Memory: memory, CPUs: size.CPUs, Level: candidate.level, Image: image}
}

func autoPlacementDisplayRowV1(candidate *autoPlacementCandidate, index int) AutoPlacementRowV1 {
	verdict := "fits"
	if index == 0 {
		verdict = "chosen"
	}
	row := AutoPlacementRowV1{Host: candidate.host.Name, Eligible: true, Verdict: verdict,
		Reason: candidate.reason, Wake: autoPlacementWakeNameV1(candidate.wakeClass), Level: candidate.level,
		Room: candidate.room, RoomKnown: !candidate.stopped, Image: "-"}
	if candidate.need.ImageDigest != "" {
		row.Image = "missing"
		if candidate.imageCached {
			row.Image = "cached"
		}
	}
	if !candidate.stopped {
		if candidate.observation != nil {
			row.RoundTrip, row.RoundTripKnown = candidate.observation.roundTrip, true
		}
		if candidate.cached != nil {
			row.RoundTrip, row.RoundTripKnown = candidate.cached.entry.RoundTrip, true
		}
	}
	return row
}
