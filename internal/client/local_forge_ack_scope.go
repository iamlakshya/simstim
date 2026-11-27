package client

import "github.com/iamlakshya/simstim/internal/api"

func (s *localForgeAckSelection) captureScope(p jsonValueProjection, span api.JSONSpan) error {
	var fields [7]api.JSONSpan
	if err := p.Object(span, sandboxReceiptScopeFields, func(f api.JSONField) error {
		if f.Known >= 0 {
			fields[f.Known] = f.Value
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range []struct {
		span api.JSONSpan
		want string
	}{{fields[0], s.facts.scope.Host}, {fields[2], s.facts.scope.HostID}, {fields[3], s.facts.scope.MemberID}} {
		equal, err := p.StringEqual(item.span, item.want)
		if err != nil {
			return err
		}
		if !equal {
			return sandboxReceiptError()
		}
	}
	if err := matchForgeAckSource(p, fields[1], s.facts.scope.Source); err != nil {
		return err
	}
	var err error
	s.facts.scope.RootID, err = forgeAckFixedString(p, fields[4], 32)
	if err != nil {
		return err
	}
	s.facts.scope.RunGeneration, err = forgeAckFixedString(p, fields[5], 32)
	if err != nil {
		return err
	}
	s.facts.scope.BackendGeneration, err = forgeAckFixedString(p, fields[6], 32)
	if err != nil {
		return err
	}
	return s.facts.scope.validate()
}

// Compare historical scalar spans with the actual original checked Source.
// No encoded Source or native Reference is deserialised into authority here.
func matchForgeAckSource(p jsonValueProjection, span api.JSONSpan, actual hostStateSourceWitness) error {
	if err := actual.validate(); err != nil {
		return err
	}
	var fields [5]api.JSONSpan
	if err := p.Object(span, hostStateSourceWitnessFields, func(f api.JSONField) error {
		if f.Known < 0 {
			return futureSandboxReceiptError()
		}
		fields[f.Known] = f.Value
		return nil
	}); err != nil {
		return err
	}
	var root [6]api.JSONSpan
	if err := p.Object(fields[4], hostStateSourceRootFields, func(f api.JSONField) error {
		if f.Known < 0 {
			return futureSandboxReceiptError()
		}
		root[f.Known] = f.Value
		return nil
	}); err != nil {
		return err
	}
	for _, item := range []struct {
		span api.JSONSpan
		want string
	}{{fields[2], actual.Generation}, {fields[3], actual.Path}, {root[1], actual.Root.Identity}} {
		equal, err := p.StringEqual(item.span, item.want)
		if err != nil {
			return err
		}
		if !equal {
			return sandboxReceiptError()
		}
	}
	for _, item := range []struct {
		span     api.JSONSpan
		want     int64
		unsigned bool
	}{{fields[0], int64(actual.Version), false}, {root[0], int64(actual.Root.Version), false}, {root[2], int64(actual.Root.Mode), true}} {
		value, err := receiptProjectionInteger(p, item.span, item.unsigned)
		if err != nil {
			return err
		}
		if value != item.want {
			return sandboxReceiptError()
		}
	}
	for _, item := range []struct {
		span api.JSONSpan
		want bool
	}{{fields[1], actual.Exists}, {root[5], actual.Root.Directory}} {
		value, err := receiptProjectionBoolean(p, item.span)
		if err != nil {
			return err
		}
		if value != item.want {
			return sandboxReceiptError()
		}
	}
	// The full canonical Source check compares directory identity/mode,
	// never directory size or mtime: unrelated entry changes do not change
	// membership generation. The shared receipt pass already validated the
	// historical size/time values as known metadata; keep that exact rule.
	return nil
}
