package client

import "reflect"

// SecretDefinition is a local command and optional public secret metadata.
// TOML accepts the legacy command string or a table with these exact fields.
// Nil Hosts and OnHosts mean absent. A nonnil empty OnHosts denies every host.
type SecretDefinition struct {
	Run         string   `toml:"run"`
	Hosts       []string `toml:"hosts"`
	Header      string   `toml:"header,omitempty"`
	Format      string   `toml:"format,omitempty"`
	Kind        string   `toml:"kind,omitempty"`
	OnHosts     []string `toml:"on_hosts"`
	formatEmpty bool
}

// DecodeSecretDefinition shares the config inspector's schema resolution.
// It never executes a command or accepts a value, Store, source or prefix.
func DecodeSecretDefinition(raw any) (SecretDefinition, error) {
	if command, ok := raw.(string); ok {
		return SecretDefinition{Run: command}, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return SecretDefinition{}, secretDefinitionError("a secret must be a command string or a definition table")
	}
	view := InspectConfigTable(table, reflect.TypeFor[SecretDefinition]())
	for _, key := range view.Keys {
		if key.Conflict != "" {
			return SecretDefinition{}, secretDefinitionError("a secret definition has " + key.Conflict)
		}
		if key.Type == nil {
			return SecretDefinition{}, secretDefinitionError("a secret definition contains an unknown field")
		}
	}
	var output SecretDefinition
	for _, key := range view.Keys {
		if key.Name == "hosts" || key.Name == "on_hosts" {
			values, ok := secretDefinitionStrings(table[key.Spelling])
			if !ok {
				return SecretDefinition{}, secretDefinitionError("secret " + key.Name + " must be an array of strings")
			}
			if key.Name == "hosts" {
				output.Hosts = values
			} else {
				output.OnHosts = values
			}
			continue
		}
		value, ok := table[key.Spelling].(string)
		if !ok {
			return SecretDefinition{}, secretDefinitionError("secret " + key.Name + " must be a string")
		}
		switch key.Name {
		case "run":
			output.Run = value
		case "header":
			output.Header = value
		case "format":
			output.Format = value
			output.formatEmpty = value == ""
		case "kind":
			output.Kind = value
		}
	}
	return output, nil
}

// UnmarshalTOML publishes only a complete valid union value. Failed decoding
// leaves an existing definition and every slice it owns unchanged.
func (definition *SecretDefinition) UnmarshalTOML(raw any) error {
	if definition == nil {
		return secretDefinitionError("a secret definition needs a destination")
	}
	next, err := DecodeSecretDefinition(raw)
	if err != nil {
		return err
	}
	*definition = next
	return nil
}

func secretDefinitionStrings(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		output := make([]string, len(values))
		copy(output, values)
		return output, true
	case []any:
		output := make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			output[index] = text
		}
		return output, true
	default:
		return nil, false
	}
}

func cloneSecretStrings(input []string) []string {
	if input == nil {
		return nil
	}
	output := make([]string, len(input))
	copy(output, input)
	return output
}

func secretDefinitionError(message string) *UserError {
	return &UserError{Code: "bad_config", Msg: message,
		Hint: "Use a command string, or a table with run, hosts, header, format, kind and on_hosts in config.toml."}
}
