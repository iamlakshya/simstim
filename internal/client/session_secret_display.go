package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"slices"
)

// SecretPresentation contains owned name lists for display. It grants no
// name, fetch, exposure or arming action and contains no secret value.
type SecretPresentation struct {
	Exposed []string
	Unknown []string
	Unarmed []string
}

// PresentSessionSecrets uses only the selected member's complete compact
// summary. Missing, future or partial metadata cannot imply exposure/arming.
func PresentSessionSecrets(session Session) SecretPresentation {
	names := slices.Clone(session.Secrets)
	slices.Sort(names)
	names = slices.Compact(names)
	summary := session.SecretSummary
	complete := summary != nil && summary.Supported() && summary.Generation != "" && len(summary.Secrets) == len(names)
	if complete {
		for _, name := range names {
			if _, present := summary.Secrets[name]; !present {
				complete = false
				break
			}
		}
	}
	if !complete {
		return SecretPresentation{Unknown: names}
	}
	var result SecretPresentation
	for _, name := range names {
		status := summary.Secrets[name]
		if status.Mode == api.SecretModeExposed {
			result.Exposed = append(result.Exposed, name)
		}
		if !status.Armed {
			result.Unarmed = append(result.Unarmed, name)
		}
	}
	return result
}
