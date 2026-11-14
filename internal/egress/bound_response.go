package egress

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// Only the actual request plan may create this context under registry
// admission. It carries no value, grant or guest-supplied authority.
type secretRequestUse struct {
	Names, Identities []string
}

func (secretRequestUse) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func registryResponseSnapshots(state *memberState) (map[string]*responseEchoGroupSnapshot, error) {
	groups := make(map[string]map[api.Secret][]responseEchoChoice)
	add := func(authority string, value, sentinel api.Secret, identity string, pending bool) {
		if groups[authority] == nil {
			groups[authority] = make(map[api.Secret][]responseEchoChoice)
		}
		choice := responseEchoChoice{Identity: identity, Sentinel: sentinel, Pending: pending}
		if !slices.Contains(groups[authority][value], choice) {
			groups[authority][value] = append(groups[authority][value], choice)
		}
	}
	for _, secret := range state.secrets {
		if secret.bound == nil {
			continue
		}
		for _, authority := range secret.bound.binding.Hosts {
			if secret.present {
				add(authority, secret.value, secret.bound.sentinel, secret.identity, false)
			}
			for _, prior := range secret.history {
				add(authority, prior, secret.bound.sentinel, secret.identity, false)
			}
		}
		for _, mint := range secret.minted {
			if !mint.retired {
				add(mint.authority, mint.value, mint.sentinel, secret.identity, !mint.ready)
			}
		}
	}
	output := make(map[string]*responseEchoGroupSnapshot, len(groups))
	for authority, values := range groups {
		var records []responseEchoValueGroup
		for _, value := range slices.Sorted(maps.Keys(values)) {
			records = append(records, responseEchoValueGroup{Value: value, Choices: values[value]})
		}
		snapshot, err := newResponseEchoGroupSnapshot(records)
		if err != nil {
			return nil, memberError("secret_identity", "credential state cannot preserve unambiguous stand-ins; replacement did not occur", "Stop and resume for a fresh approved generation before changing these values.")
		}
		output[authority] = snapshot
	}
	return output, nil
}

func (s *Server) responseSecretSnapshot(ctx context.Context, authority string, use secretRequestUse) (*responseEchoSnapshot, error) {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.policyMu.Unlock()
	state := s.currentMembers()
	if state == nil || state.version != MemberRegistryV2 {
		return nil, errResponseRegistry
	}
	identities, err := responseUsedIdentities(state, authority, use)
	if err != nil {
		return nil, err
	}
	if snapshot := state.echo[authority]; snapshot != nil {
		return snapshot.view(identities)
	}
	return nil, nil
}

func responseUsedIdentities(state *memberState, authority string, use secretRequestUse) ([]string, error) {
	identities := slices.Clone(use.Identities)
	slices.Sort(identities)
	identities = slices.Compact(identities)
	if len(use.Names) == 0 && len(identities) == 0 {
		return nil, nil
	}
	for _, name := range use.Names {
		found := false
		for _, identity := range identities {
			_, valid := responseVariantParent(state, authority, name, identity)
			found = found || valid
		}
		if !found {
			return nil, errResponseRegistry
		}
	}
	for _, identity := range identities {
		found := false
		for _, name := range use.Names {
			_, valid := responseVariantParent(state, authority, name, identity)
			found = found || valid
		}
		if !found {
			return nil, errResponseRegistry
		}
	}
	var parents []string
	for _, identity := range identities {
		for _, name := range use.Names {
			if parent, valid := responseVariantParent(state, authority, name, identity); valid {
				parents = append(parents, parent)
			}
		}
	}
	slices.Sort(parents)
	return slices.Compact(parents), nil
}

func responseVariantParent(state *memberState, authority, name, identity string) (string, bool) {
	secret, present := state.secrets[name]
	if !present || secret.bound == nil || !secret.present {
		return "", false
	}
	if identity == secret.identity && slices.Contains(secret.bound.binding.Hosts, authority) {
		return secret.identity, true
	}
	for _, mint := range secret.minted {
		if mint.identity == identity && mint.authority == authority && mint.ready && !mint.retired {
			return secret.identity, true
		}
	}
	return "", false
}

func responseMintOwner(state *memberState, authority string, use secretRequestUse) (string, error) {
	if len(use.Names) == 0 && len(use.Identities) == 0 {
		for _, name := range slices.Sorted(maps.Keys(state.secrets)) {
			secret := state.secrets[name]
			if secret.bound != nil && secret.present && slices.Contains(secret.bound.binding.Hosts, authority) {
				return name, nil
			}
		}
		return "", errResponseRegistry
	}
	identities, err := responseUsedIdentities(state, authority, use)
	if err != nil || len(identities) == 0 {
		return "", errResponseRegistry
	}
	names := slices.Clone(use.Names)
	slices.Sort(names)
	return names[0], nil
}
