package host

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureNamedSSHKeysV1) }

// ApplyNamedSSHKeyPolicy changes only the canonical next-start name list.
// It cannot install a signer or obtain a private key.
func ApplyNamedSSHKeyPolicy(layers []api.Layer, name string, allow bool) (api.Policy, []api.Layer, error) {
	if api.CheckSecretName(name, false) != api.SecretNameOK {
		return api.Policy{}, nil, UserError("bad_secret", "the SSH key name is invalid", "Name one configured SSH key.")
	}
	next := api.CloneLayers(layers)
	index := slices.IndexFunc(next, func(layer api.Layer) bool { return layer.Kind == api.LayerSession })
	if index < 0 {
		next = append(next, api.Layer{Kind: api.LayerSession})
		index = len(next) - 1
	}
	entries := next[index].Sandbox.SSHKeys
	entries = slices.DeleteFunc(entries, func(value string) bool { return value == name || value == "!"+name })
	entry := "!" + name
	if allow {
		entry = name
	}
	next[index].Sandbox.SSHKeys = append(entries, entry)
	policy, cleaned, err := egress.MergeLayers(next)
	if err != nil {
		return api.Policy{}, nil, err
	}
	if slices.Contains(policy.Sandbox.SSHKeys, name) != allow {
		return api.Policy{}, nil, UserError("policy_locked", "a locked layer refused this SSH key selection", "Change the owning lock before selecting this SSH key.")
	}
	return policy, cleaned, nil
}

func (b *bridge) handleNamedSSHKeys(req proto.Request) bool {
	if req.Op != "ssh-key.policy" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	var args api.NamedSSHKeyPolicyArgs
	if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.V != 1 || !api.ValidSessionID(args.Session) || api.CheckSecretName(args.Name, false) != api.SecretNameOK {
		b.reply(req.ID, nil, UserError("bad_request", "invalid next-start SSH key selection", "Name one configured SSH key and session."))
		return true
	}
	if alive(args.Session) {
		result, err := HolderRequestWithFeatures(args.Session, req.Op, args, 5*time.Second, api.FeatureNamedSSHKeysV1)
		b.reply(req.ID, result, err)
		return true
	}
	result, err := changeOfflineNamedSSHKeys(args)
	b.reply(req.ID, result, err)
	return true
}

func changeOfflineNamedSSHKeys(args api.NamedSSHKeyPolicyArgs) (api.NamedSSHKeyPolicyResult, error) {
	primary, err := SandboxPrimaryID(args.Session)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	if alive(primary) {
		raw, err := HolderRequestWithFeatures(primary, "ssh-key.policy", args, 5*time.Second, api.FeatureNamedSSHKeysV1)
		var result api.NamedSSHKeyPolicyResult
		if err != nil {
			return result, err
		}
		if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.V != 1 || result.Applies != "next_start" {
			return result, UserError("protocol", "the sandbox policy reply is invalid", "Update both helpers and retry the policy command.")
		}
		return result, nil
	}
	unlock, err := lockSessionWait(primary, 5*time.Second)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	defer unlock()
	current, err := SandboxPrimaryID(args.Session)
	if err != nil || current != primary {
		return api.NamedSSHKeyPolicyResult{}, snapshotChanged()
	}
	if alive(primary) {
		return api.NamedSSHKeyPolicyResult{}, UserError("sandbox_busy", "the sandbox started during its policy edit", "Retry the policy command against its current supervisor.")
	}
	manifest, err := LoadManifest(primary)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, UserError("policy_read", "the selected session record cannot be read", "Check the session record and try again.")
	}
	if manifest.Wall == nil {
		return api.NamedSSHKeyPolicyResult{}, UserError("not_walled", "this session has no sandbox SSH key selection", "Use named keys with a walled session.")
	}
	policy, layers, err := ApplyNamedSSHKeyPolicy(manifest.Layers, args.Name, args.Allow)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	manifest.Policy, manifest.Layers = &policy, layers
	if err := saveOfflinePolicy(manifest); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	names := slices.Clone(policy.Sandbox.SSHKeys)
	slices.Sort(names)
	return api.NamedSSHKeyPolicyResult{V: 1, Names: names, Applies: "next_start"}, nil
}

// liveNamedSSHKeys projects only the actual agent and its separate next-start
// selection. Saved names cannot stand in for an installed signer.
func liveNamedSSHKeys(info *SessionInfo) {
	info.NamedSSHKeys = nil
	if !info.Alive || info.Wall == nil {
		return
	}
	raw, err := HolderRequestWithFeatures(info.ID, "ssh-key.status", struct{}{}, 500*time.Millisecond, api.FeatureNamedSSHKeysV1)
	if err != nil {
		return
	}
	var view api.NamedSSHKeyViewV1
	if !api.ExactJSONFields(raw, view) || json.Unmarshal(raw, &view) != nil || !view.Valid() {
		return
	}
	info.NamedSSHKeys = api.CloneNamedSSHKeyViewV1(&view)
}
