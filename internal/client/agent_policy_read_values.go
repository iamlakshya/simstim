package client

import (
	"context"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
)

func projectedTier(p jsonValueProjection, span api.JSONSpan) (Tier, error) {
	var value [9]byte // Longest accepted result is "throwaway".
	n, phase := 0, 0
	err := p.WalkString(span, func(r rune) error {
		if unicode.IsSpace(r) {
			if phase == 1 {
				phase = 2
			}
			return nil
		}
		if phase == 2 {
			return badSandboxState()
		}
		phase = 1
		r = unicode.ToLower(r)
		if r > 127 || n == len(value) {
			return badSandboxState()
		}
		value[n], n = byte(r), n+1
		return nil
	})
	if err != nil {
		return TierUnknown, err
	}
	tier, err := ParseTier(string(value[:n]))
	if err != nil {
		return TierUnknown, badSandboxState()
	}
	return tier, nil
}

func projectedSecretName(p jsonValueProjection, span api.JSONSpan) error {
	first := true
	err := p.WalkString(span, func(r rune) error {
		letter := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_'
		if !letter && (first || r < '0' || r > '9') {
			return badSandboxState()
		}
		first = false
		return nil
	})
	if err != nil || first {
		if err != nil {
			return err
		}
		return badSandboxState()
	}
	return nil
}

func projectedOptionalBoolean(p jsonValueProjection, span api.JSONSpan) (bool, error) {
	if span.End == 0 {
		return false, nil
	}
	for _, spelling := range []string{"null", "false", "true"} {
		yes, err := p.Literal(span, spelling)
		if err != nil {
			return false, err
		}
		if yes {
			return spelling == "true", nil
		}
	}
	return false, badSandboxState()
}

func projectAgentRow(ctx context.Context, p jsonValueProjection, span, key api.JSONSpan, schema int, selected bool) (agentSandboxPolicy, error) {
	known := []string{"session_id", "host_id", "ceiling", "ssh_agent", "secrets", "created_at", "request", "wall_intent", "policy_sources", "lineage", "delivery_intent"}
	var fields [11]api.JSONSpan
	var facts agentSandboxPolicy
	validity := agentPolicyRowValidity{wallNone: true}
	err := p.Object(span, known, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return facts, err
	}
	order, err := p.CompareStrings(fields[0], key)
	if err != nil || order != 0 {
		return facts, badSandboxState()
	}
	facts.HostID, validity.hostIDNonempty, err = projectedHostID(p, fields[1], selected)
	if err != nil {
		return facts, err
	}
	facts.Ceiling, err = projectedTier(p, fields[2])
	if err != nil {
		return facts, err
	}
	facts.SSHAgent, err = projectedOptionalBoolean(p, fields[3])
	if err != nil {
		return facts, err
	}
	if fields[4].End != 0 {
		null, err := p.Literal(fields[4], "null")
		if err != nil {
			return facts, err
		}
		if !null {
			if err := p.Array(fields[4], func(name api.JSONSpan) error { return projectedSecretName(p, name) }); err != nil {
				return facts, err
			}
		}
	}
	if err := projectedCreationTime(p, fields[5]); err != nil {
		return facts, err
	}
	nonempty := false
	if err := p.WalkString(fields[6], func(rune) error { nonempty = true; return nil }); err != nil || !nonempty {
		return facts, badSandboxState()
	}
	if selected {
		facts.CreationWall = "none"
	}
	if schema == 1 {
		if fields[7].End != 0 {
			return facts, badSandboxState()
		}
	} else {
		facts.CreationWall, validity.wallNone, err = projectedCreationWall(p, fields[7], selected)
		if err != nil {
			return facts, err
		}
	}
	if facts.SSHAgent && !validity.wallNone {
		return facts, badSandboxState()
	}
	validity.sshAgent = facts.SSHAgent
	facts.FutureLineage, err = projectedSandboxLineage(p, fields[9], fields[4], key, schema, validity)
	if err != nil {
		return facts, err
	}
	if fields[10].End != 0 {
		allowed, err := deliveryAllowedSpan(p, fields[4], fields[9], fields[9].End != 0 && !facts.FutureLineage)
		if err != nil {
			return facts, err
		}
		exposed, err := deliveryExposedSpan(p, fields[7])
		if err != nil {
			return facts, err
		}
		_, facts.FutureDelivery, err = projectSandboxDelivery(ctx, p, fields[10], allowed, exposed, schema, sandboxDeliveryContext{hostIDNonempty: validity.hostIDNonempty, wallNone: validity.wallNone, sshAgent: facts.SSHAgent, futureLineage: facts.FutureLineage}, false)
		if err != nil {
			return facts, err
		}
	}
	return facts, nil
}

func projectedCreationWall(p jsonValueProjection, span api.JSONSpan, selected bool) (string, bool, error) {
	var fields [2]api.JSONSpan
	err := p.Object(span, []string{"level", "exposed"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	var level [7]byte
	n, err := projectedBoundedBytes(p, fields[0], level[:])
	if err != nil || !validCreationWall(string(level[:n])) {
		return "", false, badSandboxState()
	}
	wallNone := string(level[:n]) == "none"
	if fields[1].End != 0 {
		var previous api.JSONSpan
		err = p.Array(fields[1], func(name api.JSONSpan) error {
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
		})
		if err != nil {
			return "", false, err
		}
	}
	if selected {
		return string(level[:n]), wallNone, nil
	}
	return "", wallNone, nil
}
