package client

import (
	"maps"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// SecretConfigView is an owned local view, not permission to send a value.
// A missing OnHosts entry adds no restriction. A present empty entry denies
// every saved host. Its names keep their exact spelling and case.
type SecretConfigView struct {
	Commands map[string]string
	Binds    map[string]api.SecretBind
	OnHosts  map[string][]string
	Kinds    map[string]string
}

// NormaliseSecretDefinitions validates every configured definition before a
// caller may run commands. Defaults must come from canonical local agent
// data after resolving conflicting Env rows. H5 applies the saved-host and
// tier gates before commands and again before any value is published.
func NormaliseSecretDefinitions(definitions map[string]SecretDefinition, trustedAgentDefaults map[string]api.SecretBind) (SecretConfigView, error) {
	view := SecretConfigView{
		Commands: make(map[string]string, len(definitions)), Binds: make(map[string]api.SecretBind),
		OnHosts: make(map[string][]string), Kinds: make(map[string]string, len(definitions)),
	}
	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		switch checkSecretName(name, false) {
		case secretNameInvalid:
			return SecretConfigView{}, secretDefinitionError("config.toml contains an invalid secret name")
		case secretNameReserved:
			return SecretConfigView{}, reservedSocketSecretError()
		}
		definition := definitions[name]
		command := strings.TrimSpace(definition.Run)
		if !usableSecretRun(definition.Run) {
			return SecretConfigView{}, secretDefinitionError("secret " + name + " needs a usable run command")
		}
		if classifySecretKind(definition.Kind) == secretKindInvalid {
			return SecretConfigView{}, secretDefinitionError("secret " + name + " kind must be empty or ssh-key")
		}
		for _, savedHost := range definition.OnHosts {
			if !ValidHostName(savedHost) {
				return SecretConfigView{}, secretDefinitionError("secret " + name + " has an invalid saved host name in on_hosts")
			}
		}
		binding, bound, err := normaliseSecretBinding(name, definition, trustedAgentDefaults[name])
		if err != nil {
			return SecretConfigView{}, err
		}
		view.Commands[name], view.Kinds[name] = command, definition.Kind
		if definition.OnHosts != nil {
			view.OnHosts[name] = cloneSecretStrings(definition.OnHosts)
		}
		if bound {
			view.Binds[name] = binding
		}
	}
	return view, nil
}

func normaliseSecretBinding(name string, definition SecretDefinition, defaults api.SecretBind) (api.SecretBind, bool, error) {
	selected, issue := planSecretBinding(secretBindingPresence{
		HostsPresent: definition.Hosts != nil, HostsCount: len(definition.Hosts), HeaderNonempty: definition.Header != "",
		FormatPresent: definition.formatEmpty || definition.Format != "", FormatNonempty: definition.Format != "", FormatExplicitEmpty: definition.formatEmpty,
		DefaultHostsPresent: defaults.Hosts != nil, DefaultHostsCount: len(defaults.Hosts), DefaultHeaderNonempty: defaults.Header != "", DefaultFormatNonempty: defaults.Format != "",
		Kind: classifySecretKind(definition.Kind),
	})
	switch issue {
	case secretBindingEmptyFormat:
		return api.SecretBind{}, false, secretDefinitionError("secret " + name + " has an explicitly empty format")
	case secretBindingEmptyHosts:
		return api.SecretBind{}, false, secretDefinitionError("secret " + name + " has an empty hosts binding")
	case secretBindingSSHKey:
		return api.SecretBind{}, false, secretDefinitionError("secret " + name + " cannot combine ssh-key kind and HTTP binding metadata")
	}
	if !selected.Bound {
		return api.SecretBind{}, false, nil
	}
	var candidate api.SecretBind
	switch selected.Hosts {
	case secretBindingLocal:
		candidate.Hosts = cloneSecretStrings(definition.Hosts)
	case secretBindingAgent:
		candidate.Hosts = cloneSecretStrings(defaults.Hosts)
	}
	switch selected.Header {
	case secretBindingLocal:
		candidate.Header = definition.Header
	case secretBindingAgent:
		candidate.Header = defaults.Header
	}
	switch selected.Format {
	case secretBindingLocal:
		candidate.Format = definition.Format
	case secretBindingAgent:
		candidate.Format = defaults.Format
	}
	if selected.DefaultRaw {
		candidate.Format = "{value}"
	}
	canonical, err := secretbind.Canonical(candidate)
	if err != nil {
		return api.SecretBind{}, false, secretDefinitionError("secret " + name + " needs exact HTTPS hosts, a credential header and a supported format")
	}
	return canonical, true, nil
}
