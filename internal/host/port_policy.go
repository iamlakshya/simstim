package host

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// PortPolicyGrant evaluates only exact port-door rules from the saved ordered
// policy. A port claim from the guest never calls this a grant.
func PortPolicyGrant(policy *api.Policy, port int, at time.Time) (bool, time.Time, error) {
	if policy == nil || port < 1 || port > 65535 {
		return false, time.Time{}, nil
	}
	for _, rule := range policy.Rules {
		if rule.Door != api.DoorPort {
			continue
		}
		if _, err := api.ParsePortTarget(rule.Target); err != nil || rule.Methods != nil || len(rule.Tools) != 0 || rule.Mode != "" || rule.Once {
			return false, time.Time{}, UserError("bad_port", "the saved port grant is not an exact supported port rule", "Use one decimal port, without methods, mode or once, before forwarding it.")
		}
	}
	order := policy.Order
	if len(order) == 0 {
		for _, kind := range []api.LayerKind{api.LayerBuiltin, api.LayerAll, api.LayerHost, api.LayerRepo, api.LayerProfile, api.LayerSession, api.LayerLockdown} {
			order = append(order, api.LayerRef{Kind: kind})
		}
	}
	for index := len(order) - 1; index >= 0; index-- {
		ref := order[index]
		allowed, permanent := false, false
		var until time.Time
		for _, rule := range policy.Rules {
			if rule.Door != api.DoorPort || rule.Layer != ref.Kind || rule.Name != ref.Name || rule.Source != ref.Source || !rule.Until.IsZero() && !at.Before(rule.Until) {
				continue
			}
			selected, _ := api.ParsePortTarget(rule.Target)
			if selected != port {
				continue
			}
			if !rule.Allow {
				return false, time.Time{}, nil
			}
			if rule.Source != "" {
				return false, time.Time{}, UserError("bad_port", "a repository file cannot grant a forwarded port", "Grant the exact port from the laptop's trusted session policy.")
			}
			allowed = true
			permanent = permanent || rule.Until.IsZero()
			if rule.Until.After(until) {
				until = rule.Until
			}
		}
		if allowed {
			if permanent {
				until = time.Time{}
			}
			return true, until, nil
		}
	}
	return false, time.Time{}, nil
}
