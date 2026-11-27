package api

import (
	"fmt"
	"io"
	"maps"
	"slices"
)

// FeatureNamedSSHKeysV1 marks actual memory-only named-key agent support.
const FeatureNamedSSHKeysV1 = "named-ssh-keys-v1"

// CodeNamedSSHKeyRefused marks a refused next-start key selection.
const CodeNamedSSHKeyRefused = "ssh_key_refused"

// DoorSSHKey names an SSH key selection, never a network allowance.
const DoorSSHKey Door = "ssh-key"

// NamedSSHKeysV1 is a private delivery from one original local cold-start grant.
// Public policy names and this data format alone cannot authorise a key fetch.
// Values never enter the ordinary secret registry or guest environment.
type NamedSSHKeysV1 struct {
	Version int               `json:"version"`
	Names   []string          `json:"names"`
	Values  map[string]Secret `json:"values"`
}

// Format hides the private delivery for every diagnostic formatting verb.
func (NamedSSHKeysV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// CloneNamedSSHKeysV1 owns the private name and value maps for one delivery.
func CloneNamedSSHKeysV1(input *NamedSSHKeysV1) *NamedSSHKeysV1 {
	if input == nil {
		return nil
	}
	return &NamedSSHKeysV1{Version: input.Version, Names: slices.Clone(input.Names), Values: maps.Clone(input.Values)}
}

// Valid checks only the closed shape, never grant authority.
func (keys *NamedSSHKeysV1) Valid() bool {
	if keys == nil || keys.Version != 1 || len(keys.Names) == 0 || len(keys.Names) != len(keys.Values) || !validSSHKeyNames(keys.Names) {
		return false
	}
	for _, name := range keys.Names {
		value, exists := keys.Values[name]
		if !exists || len(value) == 0 || len(value) > 64<<10 {
			return false
		}
	}
	return true
}

// NamedSSHKeyPolicyArgs changes a next-start selection without key values.
type NamedSSHKeyPolicyArgs struct {
	V       int    `json:"v"`
	Session string `json:"session"`
	Name    string `json:"name"`
	Allow   bool   `json:"allow"`
}

// NamedSSHKeyPolicyResult describes the names selected for the next start.
type NamedSSHKeyPolicyResult struct {
	V       int      `json:"v"`
	Names   []string `json:"names"`
	Applies string   `json:"applies"`
}

// NamedSSHKeyViewV1 separates the actual installed agent from the next start.
// Both lists are display data and cannot authorise local key selection.
type NamedSSHKeyViewV1 struct {
	V         int      `json:"v"`
	Installed []string `json:"installed"`
	NextStart []string `json:"next_start"`
}

// Valid checks a complete display shape, never permission to fetch keys.
func (view *NamedSSHKeyViewV1) Valid() bool {
	return view != nil && view.V == 1 && validSSHKeyNames(view.Installed) && validSSHKeyNames(view.NextStart)
}

// CloneNamedSSHKeyViewV1 makes an owned copy of public display lists, keeping
// empty lists non-nil so JSON output emits [] rather than null.
func CloneNamedSSHKeyViewV1(view *NamedSSHKeyViewV1) *NamedSSHKeyViewV1 {
	if view == nil {
		return nil
	}
	return &NamedSSHKeyViewV1{V: view.V, Installed: nonNilStrings(view.Installed), NextStart: nonNilStrings(view.NextStart)}
}

func nonNilStrings(names []string) []string {
	if len(names) == 0 {
		return []string{}
	}
	return slices.Clone(names)
}

func validSSHKeyNames(names []string) bool {
	for i, name := range names {
		if CheckSecretName(name, false) != SecretNameOK || i > 0 && name <= names[i-1] {
			return false
		}
	}
	return true
}
