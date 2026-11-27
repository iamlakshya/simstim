package host

import (
	"fmt"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This is private constructor input from actual protected records and the
// original runtime. It is not a wire endpoint or a registry value getter.
func imageSaveScanUnion(root *Manifest, members []*Manifest, originalPublic *api.SessionSecretState, decoys []api.DecoyTripwire) (map[string]api.Secret, map[string]bool, error) {
	values, names := make(map[string]api.Secret), make(map[string]bool)
	addState := func(label string, state *api.SessionSecretState) error {
		if state == nil {
			return nil
		}
		if err := egress.ValidateSavedPublicSessionState(state); err != nil {
			return imageSaveSourceFailure()
		}
		for name, entry := range state.Primary {
			names[name] = true
			if entry.Sentinel != "" {
				values[fmt.Sprintf("sentinel %s %s", name, label)] = entry.Sentinel
			}
		}
		for name, entries := range state.Minted {
			for index, entry := range entries {
				values[fmt.Sprintf("sentinel %s %s minted %d", name, label, index)] = entry.Sentinel
			}
		}
		for name, entries := range state.Retired {
			for index, entry := range entries {
				values[fmt.Sprintf("sentinel %s %s retired %d", name, label, index)] = entry.Sentinel
			}
		}
		return nil
	}
	if root == nil || !api.ValidSessionID(root.ID) || root.SandboxOf != "" {
		return nil, nil, imageSaveSourceFailure()
	}
	for _, manifest := range append([]*Manifest{root}, members...) {
		if manifest == nil || manifest.ID != root.ID && manifest.SandboxOf != root.ID {
			return nil, nil, imageSaveSourceFailure()
		}
		for _, name := range manifest.Secrets {
			if api.CheckSecretName(name, false) != api.SecretNameOK {
				return nil, nil, imageSaveSourceFailure()
			}
			names[name] = true
		}
		if err := addState(manifest.ID, manifest.SecretState); err != nil {
			return nil, nil, err
		}
	}
	if err := addState("original live root", originalPublic); err != nil {
		return nil, nil, err
	}
	if len(decoys) == 0 {
		return values, names, nil
	}
	definitions, err := proto.DecoyDefinitions()
	if err != nil || len(definitions) != len(decoys) {
		return nil, nil, imageSaveSourceFailure()
	}
	seen := map[string]bool{}
	for _, decoy := range decoys {
		index := slices.IndexFunc(definitions, func(definition proto.DecoyDefinition) bool { return definition.Name == decoy.Name })
		if index < 0 || seen[decoy.Name] {
			return nil, nil, imageSaveSourceFailure()
		}
		seen[decoy.Name] = true
		text := make([]string, len(decoy.Values))
		for index, value := range decoy.Values {
			text[index] = string(value)
		}
		if _, err := proto.RenderDecoy(decoy.Name, text); err != nil {
			return nil, nil, imageSaveSourceFailure()
		}
		for index, value := range decoy.Values {
			values[fmt.Sprintf("decoy %s %d", decoy.Name, index)] = value
		}
	}
	return values, names, nil
}
