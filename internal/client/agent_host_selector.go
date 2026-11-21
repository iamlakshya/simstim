package client

import "github.com/iamlakshya/simstim/internal/tomlinspect"

// The closed host-selector TOML union has a native streaming type adapter.
// It keeps ordinary policy value checks with the canonical layer validator;
// strings and ordered arrays never become a table or name/value catalogue.
func agentHostSelectorType(value tomlinspect.ValueCursor) error {
	stringValue := func(child tomlinspect.ValueCursor) error {
		if child.Type() != tomlinspect.ScalarValue {
			return errAgentSettingsType
		}
		kind, err := child.ScalarType()
		if err != nil {
			return err
		}
		if kind != tomlinspect.StringScalar {
			return errAgentSettingsType
		}
		return nil
	}
	if value.Type() == tomlinspect.ScalarValue {
		return stringValue(value)
	}
	if value.Type() != tomlinspect.ArrayValue {
		return errAgentSettingsType
	}
	array, err := value.Array()
	if err != nil {
		return err
	}
	return array.Walk(func(_ uint32, _ tomlinspect.PathCursor, child tomlinspect.ValueCursor) error {
		return stringValue(child)
	})
}
