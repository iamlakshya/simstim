package client

import (
	"cmp"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// matchingHostRuleRepositories keeps every matching laptop section as its own
// layer. It uses the same repository grammar as grants. The actual local input
// owner supplies origin from its checked metadata reader, never a host row.
func matchingHostRuleRepositories(settings Settings, origin string) ([]api.Layer, error) {
	patterns, err := hostRuleRepositoryPatterns(settings)
	if err != nil {
		return nil, err
	}
	if origin == "" {
		return nil, nil
	}
	normal, err := NormalizeRepo(origin)
	if err != nil {
		return nil, err
	}
	var layers []api.Layer
	for _, pattern := range patterns {
		if matchCanonicalRepoPattern(pattern.prefix, pattern.wildcard, canonicalRepo(normal)) {
			layers = append(layers, api.Layer{Kind: api.LayerRepo, Name: pattern.name,
				LayerValues: api.CloneLayerValues(repoLayerValues(settings.Repos[pattern.name]))})
		}
	}
	return layers, nil
}

type hostRuleRepositoryPattern struct {
	name     string
	prefix   canonicalRepo
	wildcard bool
}

func hostRuleRepositoryPatterns(settings Settings) ([]hostRuleRepositoryPattern, error) {
	patterns := make([]hostRuleRepositoryPattern, 0, len(settings.Repos))
	seen := make(map[struct {
		prefix   canonicalRepo
		wildcard bool
	}]bool, len(settings.Repos))
	for name := range settings.Repos {
		prefix, wildcard, err := ValidateRepoPattern(name)
		if err != nil {
			return nil, err
		}
		key := struct {
			prefix   canonicalRepo
			wildcard bool
		}{canonicalRepo(prefix), wildcard}
		if seen[key] {
			return nil, badRepoPatternError("repository sections name the same repository pattern")
		}
		seen[key] = true
		patterns = append(patterns, hostRuleRepositoryPattern{name, canonicalRepo(prefix), wildcard})
	}
	slices.SortFunc(patterns, func(a, b hostRuleRepositoryPattern) int {
		if a.wildcard != b.wildcard {
			if a.wildcard {
				return -1
			}
			return 1
		}
		if n := cmp.Compare(len(a.prefix), len(b.prefix)); n != 0 {
			return n
		}
		return cmp.Compare(a.name, b.name)
	})
	return patterns, nil
}
