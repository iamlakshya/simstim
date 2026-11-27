package client

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// This is names-only desired-next-start metadata in the existing local row.
// It never changes the immutable creation pin or the running signer. Only the
// actual cold producer applies it, before its independent value/host gates.
type namedSSHKeyChoicesV1 struct {
	Version int      `json:"v"`
	Allow   []string `json:"allow,omitempty"`
	Revoke  []string `json:"revoke,omitempty"`
}

func decodeNamedSSHKeyChoicesV1(raw json.RawMessage) (namedSSHKeyChoicesV1, error) {
	result := namedSSHKeyChoicesV1{Version: 1}
	if len(raw) == 0 {
		return result, nil
	}
	fields, err := api.JSONObjectFields(raw, "v", "allow", "revoke")
	if err != nil || fields["v"] == nil || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 {
		return namedSSHKeyChoicesV1{}, namedSSHPolicySourceError()
	}
	for _, names := range [][]string{result.Allow, result.Revoke} {
		canonical, err := canonicalGrantNames(names)
		if err != nil || !slices.Equal(canonical, names) {
			return namedSSHKeyChoicesV1{}, namedSSHPolicySourceError()
		}
	}
	for _, name := range result.Allow {
		if _, found := slices.BinarySearch(result.Revoke, name); found {
			return namedSSHKeyChoicesV1{}, namedSSHPolicySourceError()
		}
	}
	return result, nil
}

// applyNamedSSHKeyChoicesV1 projects names only. The caller must retain the
// actual local source and run every cold-start kind/on_hosts/tier/wall gate.
func applyNamedSSHKeyChoicesV1(creation *SandboxPolicy, names []string) ([]string, error) {
	if creation == nil {
		return nil, namedSSHPolicySourceError()
	}
	choices, err := decodeNamedSSHKeyChoicesV1(creation.sshKeyChoicesRaw)
	if err != nil {
		return nil, err
	}
	selected, err := canonicalGrantNames(names)
	if err != nil {
		return nil, err
	}
	selected = slices.DeleteFunc(selected, func(name string) bool {
		_, revoked := slices.BinarySearch(choices.Revoke, name)
		return revoked
	})
	for _, name := range choices.Allow {
		if !slices.Contains(selected, name) {
			selected = append(selected, name)
		}
	}
	slices.Sort(selected)
	return selected, nil
}

func recordNamedSSHKeyChoiceV1(ctx context.Context, c *Conn, creation *SandboxPolicy, name string, allow bool) error {
	r := grantRequestOf(ctx)
	if r == nil || creation == nil {
		return namedSSHPolicySourceError()
	}
	r.mu.Lock()
	plan := r.namedPolicy
	r.mu.Unlock()
	if plan == nil {
		return namedSSHPolicySourceError()
	}
	if err := plan.checkAcknowledgedLocalChoiceV1(ctx, c, creation, name, allow); err != nil {
		return err
	}
	return withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		if err := plan.checkAcknowledgedLocalChoiceV1(owned, c, creation, name, allow); err != nil {
			return err
		}
		return withSandboxFile(owned, c.Host, true, func(file *sandboxFile) error {
			raw, present := file.sessions[creation.SessionID]
			if !present {
				return namedSSHPolicySourceError()
			}
			before, err := decodeSandboxContext(owned, creation.SessionID, raw, file.schema)
			if err != nil {
				return err
			}
			if !sameSandboxCreation(&before, creation) {
				return namedSSHPolicySourceError()
			}
			choices, err := decodeNamedSSHKeyChoicesV1(before.sshKeyChoicesRaw)
			if err != nil {
				return err
			}
			choices.Allow = slices.DeleteFunc(choices.Allow, func(candidate string) bool { return candidate == name })
			choices.Revoke = slices.DeleteFunc(choices.Revoke, func(candidate string) bool { return candidate == name })
			if allow {
				choices.Allow = append(choices.Allow, name)
				slices.Sort(choices.Allow)
			} else {
				choices.Revoke = append(choices.Revoke, name)
				slices.Sort(choices.Revoke)
			}
			encoded, err := json.Marshal(choices)
			if err != nil {
				return err
			}
			fields, err := sandboxObjectContext(owned, raw)
			if err != nil {
				return err
			}
			fields["ssh_key_choices_v1"] = encoded
			if err := plan.checkAcknowledgedLocalChoiceV1(owned, c, creation, name, allow); err != nil {
				return err
			}
			file.sessions[creation.SessionID], err = marshalSandboxObject(fields)
			return err
		})
	})
}
