package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func (p *AutoPlacementPlanV1) filterCandidateV1(ctx context.Context, candidate *autoPlacementCandidate, inflight int64) (string, error) {
	candidate.need.ImageDigest, candidate.need.ImageBytes, candidate.need.ImageBytesKnown = "", 0, false
	filter := func() (string, error) {
		if candidate.observation != nil {
			return filterPlacementFacts(ctx, candidate, inflight, false, candidate.runtimeUpgrade)
		}
		return filterCachedPlacementFactsV1(ctx, candidate, inflight, candidate.stopped, candidate.runtimeUpgrade)
	}
	reason, err := filter()
	if err != nil || reason != "" || candidate.level != "microvm" {
		return reason, err
	}
	if reason, err := resolveAutoPlacementImageV1(ctx, candidate, candidate.selectionFactsV1()); err != nil || reason != "" {
		return reason, err
	}
	return filter()
}

// Read only this laptop's existing image selection metadata. Never contact a
// registry, resolve a tag, fetch credentials or treat this as native Need.
func resolveAutoPlacementImageV1(ctx context.Context, candidate *autoPlacementCandidate, facts api.HostFacts) (string, error) {
	options, err := ResolveNewVMOptions(candidate.localArgs, "auto")
	if err != nil {
		return "", err
	}
	arch := facts.Arch
	if candidate.need.Arch != "" && candidate.need.Arch != arch {
		return "the host platform does not match", nil
	}
	var digest string
	var blocks int64
	if options.Image == "default" {
		manifest, err := ReleaseManifest()
		if err != nil {
			return "", err
		}
		item, found := manifest.Find(supply.KindImage, "agent-base", arch)
		if !found {
			return "this client has no default image for the host architecture", nil
		}
		digest, blocks = item.Digest, item.DataBlocks
	} else {
		pins, err := readImagePins(ctx)
		if err != nil {
			return "", err
		}
		savedElsewhere := false
		for _, pin := range pins.Images {
			if pin.Ref != options.Image {
				continue
			}
			if pin.HostID != "" && pin.HostID != facts.HostID {
				savedElsewhere = true
				continue
			}
			for _, platform := range pin.Platforms {
				if platform.Arch == arch {
					digest, blocks = platform.Digest, platform.DataBlocks
				}
			}
		}
		if digest == "" && savedElsewhere {
			return "the selected image belongs to another host", nil
		}
		if digest == "" {
			return "the selected image has no local pin for this architecture", nil
		}
	}
	if !image.ValidDigest(digest) {
		return "", placementNeedError()
	}
	candidate.need.ImageDigest = digest
	candidate.need.ImageBytes, candidate.need.ImageBytesKnown = 0, false
	if blocks > 0 {
		candidate.need.ImageBytes, err = image.ConvertedSizeForDataBlocksV1(blocks)
		if err != nil {
			return "", placementNeedError()
		}
		candidate.need.ImageBytesKnown = true
	}
	return "", nil
}
