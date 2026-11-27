package host

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func namedSSHStartError(message string) error {
	return UserError(api.CodeNamedSSHKeyRefused, message, "Start the sandbox through the same verified client connection with its explicitly selected named keys.")
}

func checkNewNamedSSHSelection(policy api.Policy, keys *api.NamedSSHKeysV1) error {
	selected := slices.Clone(policy.Sandbox.SSHKeys)
	slices.Sort(selected)
	if len(selected) == 0 && keys == nil {
		return nil
	}
	if !keys.Valid() || !slices.Equal(selected, keys.Names) {
		return namedSSHStartError("the cold-start SSH key delivery is missing or differs from the selected names")
	}
	return nil
}

// captureNewNamedSSHKeys retains private data only from the same captured
// bridge New, original reservation and final canonical selected names.
// It cannot adopt a saved Manifest or create an original request witness.
func captureNewNamedSSHKeys(ctx context.Context, operation *SessionOperation, manifest *Manifest, original NewArgs) error {
	keys := original.NamedSSHKeys
	if keys == nil {
		return nil
	}
	if ctx == nil || ctx.Err() != nil || !keys.Valid() || operation == nil || manifest == nil || original.ID != manifest.ID || manifest.Policy == nil || manifest.SandboxOf != "" || manifest.Wall == nil || manifest.Wall.Driver == "plain" || !operation.Valid(manifest.ID) || operation.native.borrowed {
		return namedSSHStartError("named SSH keys have no original cold-start reservation")
	}
	selected := slices.Clone(manifest.Policy.Sandbox.SSHKeys)
	slices.Sort(selected)
	if !slices.Equal(selected, keys.Names) {
		return namedSSHStartError("the named SSH key delivery differs from the original cold selection")
	}
	for _, name := range keys.Names {
		if _, ordinary := original.Secrets[name]; ordinary {
			return namedSSHStartError("an SSH key cannot also enter ordinary secret delivery")
		}
	}
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if witness == nil {
		return namedSSHStartError("named SSH keys need their original authenticated New request")
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		return err
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	valid := witness.bridge != nil && witness.bridge.ctx != nil && witness.bridge.ctx.Err() == nil && witness.requestID != 0 && witness.captured && !witness.written && (witness.bound == nil || witness.bound == operation) && bytes.Equal(encoded, witness.encoded)
	if !valid {
		return namedSSHStartError("the original named SSH key request changed or ended")
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.namedSSHKeys != nil || operation.namedSSHKeysConsumed {
		return namedSSHStartError("the cold-start SSH key delivery was consumed or ended")
	}
	operation.namedSSHKeys = api.CloneNamedSSHKeysV1(keys)
	witness.bound = operation
	return nil
}

// takeNamedSSHKeys belongs only to the same original native startup sender.
// No public caller can request values through this private operation method.
func (operation *SessionOperation) takeNamedSSHKeys(ctx context.Context) (*api.NamedSSHKeysV1, error) {
	if operation == nil || ctx == nil {
		return nil, sessionLeaseError("the named-key sender has no original operation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() {
		return nil, sessionLeaseError("the original named-key sender ended")
	}
	if operation.namedSSHKeysConsumed {
		return nil, sessionLeaseError("the original named-key delivery was already consumed")
	}
	if operation.namedSSHKeys == nil {
		return nil, nil
	}
	operation.namedSSHKeysConsumed = true
	keys := operation.namedSSHKeys
	operation.namedSSHKeys = nil
	return keys, nil
}

func (operation *SessionOperation) clearNamedSSHKeys() {
	operation.gate.Lock()
	if operation.namedSSHKeys != nil {
		clear(operation.namedSSHKeys.Values)
		operation.namedSSHKeys = nil
	}
	operation.gate.Unlock()
}
