package client

import "github.com/iamlakshya/simstim/internal/api"

// Full and FD readers share ONE known version/name/kind grammar. Unselected
// rows retain only source spans, never their allowed-name arrays.
func projectedSandboxLineage(p jsonValueProjection, span, secrets, key api.JSONSpan, schema int, validity agentPolicyRowValidity) (bool, error) {
	if span.End == 0 {
		return false, nil
	}
	_, future, allowed, err := projectSandboxLineageData(p, span, schema, validity.sshAgent, func(field api.JSONSpan) (bool, error) {
		order, err := p.CompareStrings(field, key)
		return order == 0, err
	}, false)
	if err != nil || future {
		return future, err
	}
	if !validity.hostIDNonempty || validity.wallNone {
		return false, badSandboxState()
	}
	if secrets.End == 0 {
		return false, nil
	}
	null, err := p.Literal(secrets, "null")
	if err != nil || null {
		return false, err
	}
	contained, err := p.OrderedStringArraySubset(secrets, allowed)
	if err != nil {
		return false, err
	}
	if !contained {
		return false, badSandboxState()
	}
	return false, nil
}
