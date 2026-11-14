package client

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

// MinimumWallFloor is the weakest floor that the host's tier permits.
// An unknown tier has the same limits as shared.
func MinimumWallFloor(t Tier) string {
	if t == TierOwn || t == TierThrowaway {
		return "process"
	}
	return "gvisor"
}

// BestWallLevel is the strongest runnable driver that the host reported.
// Unknown levels and failed probes can never raise the result.
func BestWallLevel(probe api.CapProbe) wall.Level {
	best := wall.None
	for _, p := range probe.Walls {
		level, err := wall.ParseLevel(p.Level)
		if p.OK && err == nil && level > best {
			best = level
		}
	}
	return best
}

// PinWallFloor chooses a new host's floor from a fresh capability probe.
// An automatic floor may be below the tier minimum on a host that cannot
// offer it. An explicit floor must satisfy the tier policy.
func PinWallFloor(h Host, tier Tier, probe api.CapProbe, requested string) (string, error) {
	best := BestWallLevel(probe)
	if requested == "" {
		return best.String(), nil
	}
	level, err := ParseWallFloor(requested)
	if err != nil {
		return "", err
	}
	if err := Allow(h, tier, Outbound{Kind: OutboundFloor, Name: level.String()}, Settings{}); err != nil {
		return "", err
	}
	if level > best {
		return "", floorAboveBest(h, level, best)
	}
	return level.String(), nil
}

func floorAboveBest(h Host, want, best wall.Level) error {
	return &UserError{Msg: fmt.Sprintf("%s gives the %s level, so its floor cannot be %s", hostDisplayName(h), best, want),
		Hint: brand.Name + " host show " + hostCommandArg(h), Code: "level_below", Kind: KindRefused}
}

// raiseWallFloorForTier changes only the entry supplied by the tier transaction.
// It takes no locks and performs no IO. The caller saves this stricter entry
// before it publishes a lower tier, under H4's existing membership locks.
func raiseWallFloorForTier(h Host, tier Tier) (Host, error) {
	old := wall.None
	if h.WallFloor != "" {
		var err error
		if old, err = ParseWallFloor(h.WallFloor); err != nil {
			return Host{}, err
		}
	}
	minimum, _ := wall.ParseLevel(MinimumWallFloor(tier))
	if old < minimum {
		h.WallFloor = minimum.String()
	}
	return h, nil
}
