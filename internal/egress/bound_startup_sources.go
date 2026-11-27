package egress

import (
	"context"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func validateStartupSources(ctx context.Context, args BoundInitArgs) (map[string][]string, map[string]string, error) {
	seed := map[string]bool{"": true}
	if _, known := agents.Find(args.Initial.Agent); known {
		seed[args.Initial.Agent] = true
	}
	kinds, err := canonicalAgentKinds(args.Initial.AgentKinds)
	if err != nil {
		return nil, nil, err
	}
	for _, kind := range kinds {
		seed[kind] = true
	}
	fresh := 0
	for name, mode := range args.Modes {
		if _, present := args.Initial.Secrets[name]; present && mode == api.SecretModeBound {
			fresh++
		}
	}
	if len(args.SourceKinds) != fresh {
		return nil, nil, startupSourcesError()
	}
	owned := make(map[string][]string, len(args.SourceKinds))
	prefixes := make(map[string]string, len(args.SourceKinds))
	for name, sources := range args.SourceKinds {
		if err := sentinelContext(ctx, name); err != nil {
			return nil, nil, err
		}
		_, present := args.Initial.Secrets[name]
		if !present || args.Modes[name] != api.SecretModeBound || len(sources) == 0 {
			return nil, nil, startupSourcesError()
		}
		selected := slices.Clone(sources)
		slices.Sort(selected)
		for i, kind := range selected {
			if !seed[kind] || i > 0 && selected[i-1] == kind {
				return nil, nil, startupSourcesError()
			}
		}
		owned[name] = selected
		if _, saved := args.Saved[name]; !saved {
			prefix, err := startupSecretPrefix(name, selected)
			if err != nil {
				return nil, nil, err
			}
			prefixes[name] = prefix
		}
	}
	return owned, prefixes, nil
}

func startupSecretPrefix(name string, kinds []string) (string, error) {
	prefix := ""
	for _, kind := range kinds {
		required, err := registryPrefix(kind, name)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(required, prefix) {
			prefix = required
		} else if !strings.HasPrefix(prefix, required) {
			return "", memberError("secret_shape", "selected startup sources require conflicting public prefixes for secret "+name, "Use compatible selected credential formats or separate fresh sessions.")
		}
	}
	return prefix, nil
}

func cloneStartupSources(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	output := make(map[string][]string, len(input))
	for name, kinds := range input {
		output[name] = slices.Clone(kinds)
	}
	return output
}

func startupSourcesError() error {
	return memberError("bad_secret", "startup secret sources do not match the complete approved fresh bound values", "Use unique canonical selected sources from the actual startup bundle, without inferring names or agents.")
}
