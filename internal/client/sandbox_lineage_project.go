package client

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

var sandboxLineageFields = []string{"version", "parent", "root", "allowed_names", "selected_kind"}

func projectSandboxLineageData(p jsonValueProjection, span api.JSONSpan, schema int, sshAgent bool, isSelf func(api.JSONSpan) (bool, error), retain bool) (*sandboxLineage, bool, api.JSONSpan, error) {
	if schema != 3 && schema != 4 || sshAgent {
		return nil, false, api.JSONSpan{}, badSandboxState()
	}
	var fields [5]api.JSONSpan
	// Classify the version first. selected_kind was unknown v1 metadata;
	// adding v2 must not give its old case variants a new v1 alias rule.
	if err := p.Object(span, []string{"version"}, func(field api.JSONField) error {
		if field.Known == 0 {
			fields[0] = field.Value
		}
		return nil
	}); err != nil {
		return nil, false, api.JSONSpan{}, err
	}
	var encoded [19]byte
	n := 0
	if err := p.WalkEncoded(fields[0], func(part []byte) error {
		if len(part) > len(encoded)-n {
			return badSandboxState()
		}
		n += copy(encoded[n:], part)
		return nil
	}); err != nil {
		return nil, false, fields[3], err
	}
	version, err := sandboxLineageVersion(encoded[:n])
	if err != nil {
		return nil, false, fields[3], err
	}
	known := sandboxLineageFields[:4]
	if version >= 2 {
		known = sandboxLineageFields
	}
	unknown := false
	if err := p.Object(span, known, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		} else {
			unknown = true
		}
		return nil
	}); err != nil {
		return nil, false, api.JSONSpan{}, err
	}
	// A v1-only/schema3 reader cannot adopt the fresh-local form. Unknown
	// v2 extensions remain raw FutureLineage, never ordinary nil metadata.
	if version > 2 || version == 2 && (schema != 4 || unknown) {
		return nil, true, fields[3], nil
	}
	for _, field := range fields[1:3] {
		if err := projectedSessionID(p, field); err != nil {
			return nil, false, fields[3], err
		}
		self, err := isSelf(field)
		if err != nil {
			return nil, false, fields[3], err
		}
		if self {
			return nil, false, fields[3], badSandboxState()
		}
	}
	selectedKind := ""
	if version == 2 {
		for _, agent := range agents.Table {
			matches, err := p.StringEqual(fields[4], agent.Kind)
			if err != nil {
				return nil, false, fields[3], err
			}
			if matches {
				selectedKind = agent.Kind
				break
			}
		}
		if selectedKind == "" {
			return nil, false, fields[3], badSandboxState()
		}
	}
	var value *sandboxLineage
	if retain {
		value = &sandboxLineage{Version: version, SelectedKind: selectedKind, Allowed: []string{}}
		value.Parent, err = retainProjectedString(p, fields[1])
		if err != nil {
			return nil, false, fields[3], err
		}
		value.Root, err = retainProjectedString(p, fields[2])
		if err != nil {
			return nil, false, fields[3], err
		}
	}
	var previous api.JSONSpan
	err = p.Array(fields[3], func(name api.JSONSpan) error {
		if err := projectedSecretName(p, name); err != nil {
			return err
		}
		reserved, err := p.StringEqual(name, "SSH_AUTH_SOCK")
		if err != nil {
			return err
		}
		if reserved {
			return badSandboxState()
		}
		if previous.End != 0 {
			order, err := p.CompareStrings(previous, name)
			if err != nil {
				return err
			}
			if order >= 0 {
				return badSandboxState()
			}
		}
		previous = name
		if retain {
			kept, err := retainProjectedString(p, name)
			if err != nil {
				return err
			}
			value.Allowed = append(value.Allowed, kept)
		}
		return nil
	})
	return value, false, fields[3], err
}
