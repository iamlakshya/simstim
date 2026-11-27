package supervisor

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/seedmeta"
)

// trustedJudgeMetadata copies only the original protected start manifest.
// Egress still source-checks every string before any foreign model call.
func trustedJudgeMetadata(manifest *host.Manifest, policy api.Policy) classify.Metadata {
	if manifest == nil {
		return classify.Metadata{}
	}
	metadata := classify.Metadata{Agent: manifest.Agent.Kind, Task: manifest.Task}
	if profile := policy.Sandbox.Profile; profile != nil {
		metadata.Profile = *profile
	}
	if manifest.Files != nil && !manifest.Files.Seed.Scratch {
		metadata.RepositoryRemote = seedmeta.Remote(manifest.Files.Seed.Origin)
		if ecosystems := manifest.Files.Seed.Ecosystems; seedmeta.ValidEcosystems(ecosystems) {
			metadata.Ecosystems = slices.Clone(ecosystems)
		}
	}
	return metadata
}
