package egress

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Main owns the same registry gate across this view, pure tripwire, canonical
// policy precheck, credential pin/401, reservation and real owner tagging.
func (s *Server) requestSecretViewLocked() *requestSecretSnapshot {
	state := s.currentMembers()
	if state == nil || state.version != MemberRegistryV2 {
		return nil
	}
	return state.requestView
}

func secretRequestUseForPlan(plan requestSecretPlan) secretRequestUse {
	return secretRequestUse{Names: plan.usedNames(), Identities: plan.usedIdentities()}
}

// Resource dependencies are separate from actual credential use. In
// particular an anonymous response can contain any bound credential echo.
func (s *Server) boundSecretNamesLocked(authority string) []string {
	state := s.currentMembers()
	if state == nil || state.version != MemberRegistryV2 {
		return nil
	}
	var names []string
	for _, name := range slices.Sorted(maps.Keys(state.secrets)) {
		secret := state.secrets[name]
		if secret.bound != nil && slices.Contains(secret.bound.binding.Hosts, authority) {
			names = append(names, name)
		}
	}
	for name, entries := range state.retired {
		for _, entry := range entries {
			if slices.Contains(entry.binding.Hosts, authority) {
				names = append(names, name)
				break
			}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return names
}

func (state *memberState) hasSecretResource(name, authority string) bool {
	secret, present := state.secrets[name]
	if present && secret.bound != nil && slices.Contains(secret.bound.binding.Hosts, authority) {
		return true
	}
	for _, entry := range state.retired[name] {
		if slices.Contains(entry.binding.Hosts, authority) {
			return true
		}
	}
	return false
}

// This public-stand-in-only precheck has no named-header exemption. Main
// calls it on the original DNS/SNI/authority before resolution or origin IO,
// while holding the same gate as the existing canonical policy admission.
func (s *Server) checkSecretDestinationLocked(raw string) *proto.Error {
	view := s.requestSecretViewLocked()
	if view == nil {
		return nil
	}
	if failure := view.tripwire(raw, true); failure != nil {
		return failure.detail
	}
	return nil
}
