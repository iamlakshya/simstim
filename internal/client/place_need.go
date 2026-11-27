package client

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// PlacementNeed contains selection metadata, not an admission grant. Memory
// is the guest/session size; the selected wall's complete unit cap is derived
// with the same canonical VM reserve formula before comparing host room.
type PlacementNeed struct {
	Arch            string
	Tier            Tier
	Features        []string
	MinLevel        string
	Memory, Disk    int64
	ImageDigest     string
	ImageBytes      int64
	ImageBytesKnown bool
	RuntimeDigests  []string
	Untrusted       bool
	RequiresOwn     bool
}

// ParsePlacementNeed parses only --need metadata. A real local start planner
// must supply its original layer/agent size, wall, image and secret minimums.
func ParsePlacementNeed(values []string) (PlacementNeed, error) {
	var need PlacementNeed
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			key, text, pair := strings.Cut(part, "=")
			if pair {
				switch key {
				case "arch":
					if text != "amd64" && text != "arm64" || need.Arch != "" && need.Arch != text {
						return need, placementNeedError()
					}
					need.Arch = text
				case "tier":
					tier, err := ParseTier(text)
					if err != nil || need.Tier != TierUnknown && need.Tier != tier {
						return need, placementNeedError()
					}
					need.Tier = tier
				default:
					return need, placementNeedError()
				}
			} else {
				if !validPlacementFeature(part) {
					return need, placementNeedError()
				}
				need.Features = append(need.Features, part)
			}
		}
	}
	slices.Sort(need.Features)
	need.Features = slices.Compact(need.Features)
	return need, nil
}

func placementNeedError() error {
	return &UserError{Msg: "the automatic placement need is invalid",
		Hint: "Use --need arch=amd64, tier=own|throwaway|shared or a feature such as gpu. Automatic placement needs gvisor or microvm and the original sandbox size.", Code: "bad_need", Kind: KindUsage}
}

func validatePlacementNeed(need PlacementNeed) error {
	if need.Arch != "" && need.Arch != "amd64" && need.Arch != "arm64" || need.Tier != TierUnknown && !need.Tier.Valid() ||
		need.MinLevel != "gvisor" && need.MinLevel != "microvm" || need.Memory < api.MinMemoryMax || need.Disk < 0 || need.ImageBytes < 0 || need.RequiresOwn && need.Untrusted {
		return placementNeedError()
	}
	if need.ImageDigest != "" && !image.ValidDigest(need.ImageDigest) {
		return placementNeedError()
	}
	for _, feature := range need.Features {
		if !validPlacementFeature(feature) {
			return placementNeedError()
		}
	}
	for _, digest := range need.RuntimeDigests {
		if !supply.IsHex64(digest) {
			return placementNeedError()
		}
	}
	return nil
}

// poolPlacementReason is called BEFORE lifecycle status, dial or facts. It
// uses only the user's saved pool/features and current local tier policy.
func poolPlacementReason(host Host, tier Tier, need PlacementNeed) (string, error) {
	if err := validateHostPlacement(host); err != nil {
		return "", err
	}
	intent := ""
	if need.Untrusted {
		intent = "untrusted"
	}
	if tier == TierShared && need.Tier == TierShared {
		intent = "shared"
	}
	if tier == TierThrowaway && need.Tier == TierThrowaway {
		intent = "throwaway"
	}
	if err := Allow(host, tier, Outbound{Kind: OutboundPool, Name: intent}, DefaultSettings()); err != nil {
		return "not in the auto pool", nil
	}
	if need.RequiresOwn && tier != TierOwn || need.Tier != TierUnknown && tier < need.Tier {
		return "the local secret or requested tier needs another host tier", nil
	}
	for _, feature := range need.Features {
		if !slices.Contains(host.Place.Features, feature) {
			return "a requested local feature is missing", nil
		}
	}
	for _, feature := range host.Place.Mandatory {
		if !slices.Contains(need.Features, feature) {
			return "the request does not name a mandatory local feature", nil
		}
	}
	return "", nil
}
