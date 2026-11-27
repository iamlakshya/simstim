package client

import (
	"reflect"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type agentSecretFields struct {
	run, hosts, header, format, kind, onHosts                                           tomlinspect.ValueCursor
	runPresent, hostsPresent, headerPresent, formatPresent, kindPresent, onHostsPresent bool
}

func (v *agentSettingsVisitor) secretDefinitions() error {
	// The ordinary canonical Settings helper checks ALL global defaults,
	// even with no configured definitions. Keep that same unconditional gate.
	defaults, err := agents.GlobalSecretDefaults()
	if err != nil {
		return secretDefinitionError("the local agent secret metadata cannot be used")
	}
	definitions, present := v.schema.root.slot("secrets")
	if !present {
		return nil
	}
	table, err := definitions.Table()
	if err != nil {
		return err
	}
	return table.Walk(func(path tomlinspect.PathCursor, definition tomlinspect.ValueCursor) error {
		name, err := path.Name()
		if err != nil {
			return err
		}
		var names secretNameValidation
		if err := name.Walk(func(r rune) error { names.Rune(r); return nil }); err != nil {
			return err
		}
		switch names.Check(false) {
		case secretNameInvalid:
			return secretDefinitionError("config.toml contains an invalid secret name")
		case secretNameReserved:
			return reservedSocketSecretError()
		}
		var fallback api.SecretBind
		// These keys/rows are the finite canonical agent data. Configured
		// names stay FD-backed and exact-case; no command/name map is built.
		for candidate, metadata := range defaults {
			matched, err := name.Equal(candidate)
			if err != nil {
				return err
			}
			if matched {
				fallback = metadata.Bind
				break
			}
		}
		return v.secretDefinition(definition, fallback)
	})
}

func (v *agentSettingsVisitor) secretFields(value tomlinspect.ValueCursor) (agentSecretFields, error) {
	if value.Type() == tomlinspect.ScalarValue {
		return agentSecretFields{run: value, runPresent: true}, nil
	}
	node := v.schema.nodes[reflect.TypeFor[SecretDefinition]()]
	if node == nil {
		return agentSecretFields{}, errAgentSettingsSchema
	}
	if err := node.table(value, true); err != nil {
		return agentSecretFields{}, err
	}
	var fields agentSecretFields
	fields.run, fields.runPresent = node.slot("run")
	fields.hosts, fields.hostsPresent = node.slot("hosts")
	fields.header, fields.headerPresent = node.slot("header")
	fields.format, fields.formatPresent = node.slot("format")
	fields.kind, fields.kindPresent = node.slot("kind")
	fields.onHosts, fields.onHostsPresent = node.slot("on_hosts")
	return fields, nil
}

func (v *agentSettingsVisitor) secretDefinition(value tomlinspect.ValueCursor, defaults api.SecretBind) error {
	fields, err := v.secretFields(value)
	if err != nil {
		return err
	}
	var run secretRunValidation
	if fields.runPresent {
		text, err := agentString(fields.run)
		if err != nil {
			return err
		}
		if err := text.Walk(func(r rune) error { run.Rune(r); return nil }); err != nil {
			return err
		}
	}
	if !run.Valid() {
		return secretDefinitionError("a secret definition needs a usable run command")
	}
	var kind secretKindValidation
	if fields.kindPresent {
		text, err := agentString(fields.kind)
		if err != nil {
			return err
		}
		if err := text.Walk(func(r rune) error { kind.Rune(r); return nil }); err != nil {
			return err
		}
	}
	if kind.Kind() == secretKindInvalid {
		return secretDefinitionError("a secret kind must be empty or ssh-key")
	}
	if fields.onHostsPresent {
		array, err := fields.onHosts.Array()
		if err != nil {
			return err
		}
		if err := array.Walk(func(_ uint32, _ tomlinspect.PathCursor, item tomlinspect.ValueCursor) error {
			return validateAgentSavedHost(item)
		}); err != nil {
			return err
		}
	}
	presence := secretBindingPresence{HostsPresent: fields.hostsPresent, FormatPresent: fields.formatPresent, Kind: kind.Kind(), DefaultHostsPresent: defaults.Hosts != nil, DefaultHostsCount: len(defaults.Hosts), DefaultHeaderNonempty: defaults.Header != "", DefaultFormatNonempty: defaults.Format != ""}
	if fields.hostsPresent {
		array, err := fields.hosts.Array()
		if err != nil {
			return err
		}
		count, err := array.Len()
		if err != nil {
			return err
		}
		presence.HostsCount = int(count)
	}
	if fields.headerPresent {
		text, err := agentString(fields.header)
		if err != nil {
			return err
		}
		empty, err := text.Equal("")
		if err != nil {
			return err
		}
		presence.HeaderNonempty = !empty
	}
	if fields.formatPresent {
		text, err := agentString(fields.format)
		if err != nil {
			return err
		}
		empty, err := text.Equal("")
		if err != nil {
			return err
		}
		presence.FormatNonempty, presence.FormatExplicitEmpty = !empty, empty
	}
	selected, issue := planSecretBinding(presence)
	switch issue {
	case secretBindingEmptyFormat:
		return secretDefinitionError("a secret definition has an explicitly empty format")
	case secretBindingEmptyHosts:
		return secretDefinitionError("a secret definition has an empty hosts binding")
	case secretBindingSSHKey:
		return secretDefinitionError("a secret cannot combine ssh-key kind and HTTP binding metadata")
	}
	if !selected.Bound {
		return nil
	}
	return v.secretBinding(fields, defaults, selected)
}
