package client

import "github.com/iamlakshya/simstim/internal/api"

// Project both known and future lineage without materialising names or
// unknown bodies. Every unrelated row uses the same version grammar.
func projectedSandboxLineage(p *api.JSONProjection, span, secrets, key api.JSONSpan, schema int, validity agentPolicyRowValidity) (bool, error) {
	if span.End == 0 {
		return false, nil
	}
	if schema != 3 && schema != 4 || validity.sshAgent {
		return false, badSandboxState()
	}
	var fields [4]api.JSONSpan
	if err := p.Object(span, []string{"version", "parent", "root", "allowed_names"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	}); err != nil {
		return false, err
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
		return false, err
	}
	version, err := sandboxLineageVersion(encoded[:n])
	if err != nil {
		return false, err
	}
	if version > 1 {
		return true, nil
	}
	if !validity.hostIDNonempty || validity.wallNone {
		return false, badSandboxState()
	}
	for _, field := range fields[1:3] {
		if err := projectedSessionID(p, field); err != nil {
			return false, err
		}
		order, err := p.CompareStrings(field, key)
		if err != nil || order == 0 {
			return false, badSandboxState()
		}
	}
	var previous api.JSONSpan
	if err := p.Array(fields[3], func(name api.JSONSpan) error {
		if err := projectedSecretName(p, name); err != nil {
			return err
		}
		reserved, err := p.StringEqual(name, "SSH_AUTH_SOCK")
		if err != nil || reserved {
			return badSandboxState()
		}
		if previous.End != 0 {
			order, err := p.CompareStrings(previous, name)
			if err != nil || order >= 0 {
				return badSandboxState()
			}
		}
		previous = name
		return nil
	}); err != nil {
		return false, err
	}
	if secrets.End == 0 {
		return false, nil
	}
	null, err := p.Literal(secrets, "null")
	if err != nil || null {
		return false, err
	}
	contained, err := p.OrderedStringArraySubset(secrets, fields[3])
	if err != nil || !contained {
		if err != nil {
			return false, err
		}
		return false, badSandboxState()
	}
	return false, nil
}
