package client

import (
	"fmt"
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// LocalAgentSelection owns one fresh local selection. It contains names only:
// no values, mode, exposure, inherited allowance or host-row authority.
// Only the canonical local new-session planner constructs it.
type LocalAgentSelection struct {
	originalAgent string
	originalArgv  []string
	resolvedArgv  []string
	host          Host
	kind          string
	names         []string
}

// Kind returns the exact selected agent kind from the canonical agent table.
func (s *LocalAgentSelection) Kind() string {
	if s == nil {
		return ""
	}
	return s.kind
}

// Names returns an owned copy of the canonical requested names.
func (s *LocalAgentSelection) Names() []string {
	if s == nil {
		return nil
	}
	return slices.Clone(s.names)
}

// SelectNewAgentSecrets selects names from materialised local laptop layers.
// The normal new path and explicit new --in share this selector. A remote
// repository-file layer is never local name-selection authority.
func SelectNewAgentSecrets(h Host, choice api.NewArgs, localLayers []api.Layer) (*LocalAgentSelection, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return nil, err
	}
	return selectNewAgentSecrets(settings, h, choice, localLayers)
}

func selectNewAgentSecrets(settings Settings, h Host, choice api.NewArgs, localLayers []api.Layer) (*LocalAgentSelection, error) {
	selected, err := agents.ResolveStart(choice.Agent, choice.Argv)
	if err != nil {
		return nil, &UserError{Msg: "the selected agent has no canonical local command", Hint: "Choose a listed agent or pass an explicit command.", Code: "bad_agent", Kind: KindUsage}
	}
	configured, err := ConfiguredSecretNames(settings)
	if err != nil {
		return nil, err
	}
	for _, layer := range localLayers {
		if layer.Source == api.SourceRepositoryFile || layer.Kind == api.LayerBuiltin {
			return nil, &UserError{Msg: "fresh secret names need local laptop policy layers", Hint: "Select the new agent from the current local configuration.", Code: "grant_intent", Kind: KindRefused}
		}
	}
	names, err := PolicySecretNames(localLayers)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !configured[name] {
			return nil, &UserError{Msg: fmt.Sprintf("no secret named %s in config.toml", name), Hint: "Configure the selected secret command before starting the new agent.", Code: "secret_not_found", Kind: KindNotFound}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return &LocalAgentSelection{host: cloneGrantHost(h), kind: selected.Kind, names: names, originalAgent: choice.Agent, originalArgv: slices.Clone(choice.Argv), resolvedArgv: slices.Clone(selected.Argv)}, nil
}

// SelectFreshAgentSecrets reads only current local default/host/profile names
// for an explicit new child. It does not refresh its running root's policy,
// repository source, resource ceiling, image, modes or credential grants.
func SelectFreshAgentSecrets(h Host, choice api.NewArgs) (*LocalAgentSelection, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return nil, err
	}
	layers, err := ResolvePolicyLayers(settings, h, "", "", api.LayerValues{})
	if err != nil {
		return nil, err
	}
	return selectNewAgentSecrets(settings, h, choice, layers)
}

// Choice returns an owned copy of the original local Agent/Argv tuple.
func (s *LocalAgentSelection) Choice() api.NewArgs {
	if s == nil {
		return api.NewArgs{}
	}
	return api.NewArgs{Agent: s.originalAgent, Argv: slices.Clone(s.originalArgv)}
}

func (s *LocalAgentSelection) matchesAgentSelection(kind string, argv []string) bool {
	if s == nil || kind != s.originalAgent || !slices.Equal(argv, s.originalArgv) {
		return false
	}
	selected, err := agents.ResolveStart(kind, argv)
	return err == nil && selected.Kind == s.kind && slices.Equal(selected.Argv, s.resolvedArgv)
}
